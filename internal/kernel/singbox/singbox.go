package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/auth"
	singJSON "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/common/ntp"
	"github.com/sagernet/sing/service"
	"golang.org/x/time/rate"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

// drainTimeout is how long stop() waits for in-flight connections to finish
// naturally after closing all listen sockets, before hard-killing them.
const drainTimeout = 5 * time.Second

// SingBox implements kernel.Kernel by embedding sing-box as a Go library.
//
// User operations (AddUsers/RemoveUsers/UpdateUsers) use sing-box's native
// UpdatableInbound interface, which hot-swaps user credentials without
// restarting listeners — zero connection disruption.
type SingBox struct {
	cfg config.KernelConfig

	mu     sync.RWMutex
	box    *box.Box
	ctx    context.Context
	cancel context.CancelFunc

	users      []model.UserSpec
	nodeConfig *model.NodeSpec
	tls        kernel.TLSCert

	// 每个 Box 独立维护连接状态，所有 Box 共享生命周期累计流量。
	connTracker *ConnTracker
	traffic     *trafficTotals

	// speedLimitFunc resolves a user UUID to a *rate.Limiter.
	// Set once by SetSpeedLimitFunc and forwarded to every new ConnTracker.
	speedLimitFunc func(string) *rate.Limiter

	// deviceLimitFunc resolves a user UUID to (limit, hasLimit) for gate-keeping.
	// Set once by SetDeviceLimitFunc and forwarded to every new ConnTracker.
	deviceLimitFunc func(string) (int, bool)
}

func New(cfg config.KernelConfig) *SingBox {
	return &SingBox{cfg: cfg, traffic: newTrafficTotals()}
}

var _ kernel.Kernel = (*SingBox)(nil)

func (s *SingBox) Name() string { return "sing-box" }

func (s *SingBox) Capabilities() kernel.Capabilities {
	return kernel.Capabilities{
		PerUserSpeedLimit:    true,
		DeviceLimit:          true,
		BuiltInTrafficStats:  false,
		AliveIPTracking:      true,
		ForceCloseConnection: false,
		ForceCloseUser:       true,
	}
}

func (s *SingBox) Protocols() []string {
	return []string{
		"vmess", "vless", "trojan", "shadowsocks",
		"hysteria", "hysteria2", "tuic", "naive", "socks", "http", "anytls", "mieru",
	}
}

func (s *SingBox) Start(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked(nodeConfig, users, tls)
}

// startLocked 先构造候选实例，释放旧实例资源后启动；失败直接返回错误。
// 同端口监听、缓存数据库等资源不能由两个 Box 同时占用。
func (s *SingBox) startLocked(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	if err := s.validateRelay(nodeConfig, users); err != nil {
		return err
	}
	cfgMap, err := buildConfig(s.cfg, nodeConfig, users, tls)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cfgMap)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	nlog.Core().Debug("sing-box config generated", "len", len(data))

	ctx, cancel := context.WithCancel(context.Background())
	ctx, err = directOutboundContext(ctx, nodeConfig.CustomOutbounds)
	if err != nil {
		cancel()
		return err
	}
	service.MustRegister[ntp.TimeService](ctx, timesync.Default())

	opts, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		cancel()
		return fmt.Errorf("parse sing-box options: %w", err)
	}

	instance, err := box.New(box.Options{
		Context: ctx,
		Options: opts,
	})
	if err != nil {
		cancel()
		return fmt.Errorf("create sing-box instance: %w", err)
	}

	// 在开放监听前注册统计器，首个连接也必须纳入同一份用户统计。
	tracker := NewConnTracker(0)
	tracker.traffic = s.traffic
	if previous := s.connTracker; previous != nil {
		// 设备快照更新时整体替换 map，可共享只读快照；沿用时间戳以保留原过期语义。
		previous.globalMu.RLock()
		tracker.globalDevices = previous.globalDevices
		tracker.globalLastUpdate = previous.globalLastUpdate
		previous.globalMu.RUnlock()
	}
	tracker.setNodeUsers(nodeConfig, users)
	if s.speedLimitFunc != nil {
		tracker.SetSpeedLimitFunc(s.speedLimitFunc)
	}
	if s.deviceLimitFunc != nil {
		tracker.SetDeviceLimitFunc(s.deviceLimitFunc)
	}
	instance.Router().AppendTracker(tracker)
	if s.box != nil {
		s.stop()
	}
	if err := instance.Start(); err != nil {
		instance.Close()
		cancel()
		return fmt.Errorf("start sing-box: %w", err)
	}

	// New instance started successfully — swap state.
	s.box = instance
	s.ctx = ctx
	s.cancel = cancel
	s.users = users
	s.nodeConfig = nodeConfig
	s.tls = tls

	s.connTracker = tracker

	nlog.Core().Debug("sing-box started", "users", len(users))
	return nil
}

// Reload 对纯路由规则变化使用热更新，其他运行配置变化完整重建，失败直接返回错误。
func (s *SingBox) Reload(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.box == nil {
		return fmt.Errorf("not running")
	}
	if err := s.validateRelay(nodeConfig, users); err != nil {
		return err
	}

	cfgMap, err := buildConfig(s.cfg, nodeConfig, users, tls)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cfgMap)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	opts, err := singJSON.UnmarshalExtendedContext[option.Options](s.ctx, data)
	if err != nil {
		return fmt.Errorf("parse options: %w", err)
	}

	previous, err := buildConfig(s.cfg, s.nodeConfig, s.users, s.tls)
	if err != nil {
		return err
	}
	// 原生绑定与旧式单地址绑定可能生成相同字段，但连接时的地址族约束不同。
	if !onlyRouteRulesChanged(previous, cfgMap) || !reflect.DeepEqual(s.nodeConfig.CustomOutbounds, nodeConfig.CustomOutbounds) {
		return s.startLocked(nodeConfig, users, tls)
	}

	router := service.FromContext[adapter.Router](s.ctx)
	if router == nil {
		return fmt.Errorf("router not available")
	}

	// 路由应用失败时保留旧配置状态，让上层能够重试同一配置。
	updater, ok := router.(interface {
		UpdateRules([]option.Rule, []option.RuleSet) error
	})
	if !ok {
		return fmt.Errorf("sing-box router does not support rule updates")
	}
	if err := updater.UpdateRules(opts.Route.Rules, opts.Route.RuleSet); err != nil {
		return fmt.Errorf("reload sing-box routes: %w", err)
	}
	nlog.Core().Debug("sing-box routing reloaded")

	// Trackers remain registered on the Router (which survives ReloadUsers).
	// Only update the user map — do NOT re-register or traffic is double-counted.
	if s.connTracker != nil {
		s.connTracker.setNodeUsers(nodeConfig, users)
	}

	nlog.Core().Debug("sing-box reloaded", "users", len(users))
	s.users = users
	s.nodeConfig = nodeConfig
	s.tls = tls
	return nil
}

func onlyRouteRulesChanged(previous, next M) bool {
	// buildConfig 每次创建独立的 route 容器，这里只移除可事务热更新的两个字段。
	for _, cfg := range []M{previous, next} {
		if route, ok := cfg["route"].(M); ok {
			delete(route, "rules")
			delete(route, "rule_set")
		}
	}
	return reflect.DeepEqual(previous, next)
}

func (s *SingBox) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
}

func (s *SingBox) stop() {
	if s.box == nil {
		return
	}

	// Step 1: close all listen sockets so no new connections are accepted.
	// im.Close() sets m.started=false so the subsequent box.Close() call is a no-op
	// for inbounds (avoids double-close), but box.Close() still tears down routers
	// and outbounds.
	if im := service.FromContext[adapter.InboundManager](s.ctx); im != nil {
		_ = im.Close()
	}

	// Step 2: wait for in-flight connections to drain naturally (best-effort).
	if s.connTracker != nil {
		deadline := time.Now().Add(drainTimeout)
		for time.Now().Before(deadline) {
			if s.connTracker.ActiveCount() == 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Step 3: hard-close everything that is still open.
	s.box.Close()
	s.box = nil
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.ctx = nil
}

func (s *SingBox) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.box != nil
}

// connTrackerLocked returns the connTracker under a read-optimised lock.
func (s *SingBox) connTrackerSafe() *ConnTracker {
	s.mu.Lock()
	ct := s.connTracker
	s.mu.Unlock()
	return ct
}

// SetSpeedLimitFunc configures per-user bandwidth throttling.
func (s *SingBox) SetSpeedLimitFunc(fn func(uuid string) *rate.Limiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.speedLimitFunc = fn
	if s.connTracker != nil {
		s.connTracker.SetSpeedLimitFunc(fn)
	}
}

// SetDeviceLimitFunc configures per-user device limit gate-keeping.
// Connections exceeding the limit are rejected at connect time.
func (s *SingBox) SetDeviceLimitFunc(fn func(uuid string) (int, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deviceLimitFunc = fn
	if s.connTracker != nil {
		s.connTracker.SetDeviceLimitFunc(fn)
	}
}

// UpdateGlobalDevices updates the global device state from panel (for multi-node).
func (s *SingBox) UpdateGlobalDevices(users map[int][]string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.connTracker != nil {
		s.connTracker.UpdateGlobalDevices(users)
	}
}

// ClearGlobalDevices clears the global device state (on WS disconnect).
func (s *SingBox) ClearGlobalDevices() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.connTracker != nil {
		s.connTracker.ClearGlobalDevices()
	}
}

// ─── User management (non-disruptive) ───────────────────────────────────────

// AddUsers hot-swaps users into running inbounds. Zero connection disruption.
func (s *SingBox) AddUsers(users []model.UserSpec) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.box == nil {
		return 0, fmt.Errorf("not running")
	}

	existing := make(map[int]struct{}, len(s.users))
	for _, u := range s.users {
		existing[u.ID] = struct{}{}
	}
	var toAdd []model.UserSpec
	for _, u := range users {
		if _, dup := existing[u.ID]; !dup {
			toAdd = append(toAdd, u)
			existing[u.ID] = struct{}{}
		}
	}
	if len(toAdd) == 0 {
		return 0, nil
	}

	merged := append(append([]model.UserSpec{}, s.users...), toAdd...)
	if err := s.reloadInboundsLocked(merged); err != nil {
		return 0, err
	}
	s.users = merged
	return len(toAdd), nil
}

// RemoveUsers hot-swaps users out of running inbounds. Zero connection disruption
// for remaining users.
func (s *SingBox) RemoveUsers(users []model.UserSpec) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.box == nil {
		return 0, fmt.Errorf("not running")
	}

	removeSet := make(map[int]struct{}, len(users))
	for _, u := range users {
		removeSet[u.ID] = struct{}{}
	}
	var kept []model.UserSpec
	removed := 0
	for _, u := range s.users {
		if _, rm := removeSet[u.ID]; rm {
			removed++
		} else {
			kept = append(kept, u)
		}
	}
	if removed == 0 {
		return 0, nil
	}

	if err := s.reloadInboundsLocked(kept); err != nil {
		return 0, err
	}
	s.users = kept
	return removed, nil
}

// UpdateUsers replaces the entire user set atomically via hot-swap.
func (s *SingBox) UpdateUsers(users []model.UserSpec) (added, removed int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.box == nil {
		return 0, 0, fmt.Errorf("not running")
	}
	if err := validateRelayUsers(s.nodeConfig, users); err != nil {
		return 0, 0, err
	}

	toAdd, toRemove := kernel.UserDiff(s.users, users)
	added, removed = len(toAdd), len(toRemove)

	if added == 0 && removed == 0 {
		// Only limits may have changed — update tracker map.
		if s.connTracker != nil {
			s.connTracker.setNodeUsers(s.nodeConfig, users)
		}
		s.users = users
		return 0, 0, nil
	}

	if err = s.reloadInboundsLocked(users); err != nil {
		return 0, 0, err
	}
	s.users = users
	return
}

// reloadInboundsLocked hot-swaps inbound users using UpdatableInbound.
// Must be called with s.mu held.
func (s *SingBox) reloadInboundsLocked(users []model.UserSpec) error {
	if err := validateRelayUsers(s.nodeConfig, users); err != nil {
		return err
	}
	cfgMap, err := buildConfig(s.cfg, s.nodeConfig, users, s.tls)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cfgMap)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	opts, err := singJSON.UnmarshalExtendedContext[option.Options](s.ctx, data)
	if err != nil {
		return fmt.Errorf("parse options: %w", err)
	}
	if s.nodeConfig.IsRelayEntry() {
		return s.reloadRelayUsersLocked(users, opts)
	}
	if err := s.updateInboundUsersLocked(opts); err != nil {
		return err
	}
	if s.connTracker != nil {
		s.connTracker.setNodeUsers(s.nodeConfig, users)
	}
	return nil
}

func (s *SingBox) updateInboundUsersLocked(opts option.Options) error {
	im := service.FromContext[adapter.InboundManager](s.ctx)
	if im == nil {
		return fmt.Errorf("inbound manager not available")
	}

	for _, inb := range opts.Inbounds {
		tag := inb.Tag
		if existing, ok := im.Get(tag); ok && existing.Type() == inb.Type {
			err := fmt.Errorf("inbound %s does not support user updates", tag)
			switch v := existing.(type) {
			case adapter.UpdatableInbound[option.VMessUser]:
				if opts, ok := inb.Options.(*option.VMessInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[option.VLESSUser]:
				if opts, ok := inb.Options.(*option.VLESSInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[option.TrojanUser]:
				if opts, ok := inb.Options.(*option.TrojanInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[option.Hysteria2User]:
				if opts, ok := inb.Options.(*option.Hysteria2InboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableShadowsocksInbound:
				if opts, ok := inb.Options.(*option.ShadowsocksInboundOptions); ok {
					err = v.UpdateUsersByOptions(opts.Users)
				}
			case adapter.UpdatableInbound[option.TUICUser]:
				if opts, ok := inb.Options.(*option.TUICInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[option.AnyTLSUser]:
				if opts, ok := inb.Options.(*option.AnyTLSInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[option.MieruUser]:
				if opts, ok := inb.Options.(*option.MieruInboundOptions); ok {
					err = v.UpdateUsers(opts.Users)
				}
			case adapter.UpdatableInbound[auth.User]:
				switch opts := inb.Options.(type) {
				case *option.NaiveInboundOptions:
					err = v.UpdateUsers(opts.Users)
				case *option.SocksInboundOptions:
					err = v.UpdateUsers(opts.Users)
				case *option.HTTPMixedInboundOptions:
					err = v.UpdateUsers(opts.Users)
				}
			}
			if err == nil {
				continue
			}
			return fmt.Errorf("update inbound %s users: %w", tag, err)
		}
		return fmt.Errorf("inbound %s not available for user updates", tag)
	}

	return nil
}

// ─── Observability ──────────────────────────────────────────────────────────
func (s *SingBox) GetUserTraffic(_ context.Context) (traffic map[int][2]int64, aliveIPs map[int]map[string]bool, connCount int, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	traffic = s.traffic.snapshot()
	if s.box != nil && s.connTracker != nil {
		_, aliveIPs, connCount = s.connTracker.GetUserTraffic()
	}
	return traffic, aliveIPs, connCount, nil
}

// CloseConnection force-closes a specific connection by its ID.
func (s *SingBox) CloseConnection(_ context.Context, connID string) error {
	ct := s.connTrackerSafe()
	if ct == nil {
		return fmt.Errorf("not running")
	}
	if !ct.CloseByID(connID) {
		nlog.Core().Debug("CloseConnection: connection not found (already closed?)", "id", connID)
	}
	return nil
}

// CloseUserConnections terminates all connections for a user UUID.
func (s *SingBox) CloseUserConnections(_ context.Context, uuid string) error {
	ct := s.connTrackerSafe()
	if ct == nil {
		return nil
	}
	ct.CloseByUUID(uuid)
	return nil
}

// buildUserMap creates a UUID→userID mapping used by ConnTracker to attribute
// connections to the correct user. All sing-box protocols use the user's UUID
// as the inbound name/username, so this covers every protocol.
func buildUserMap(users []model.UserSpec) map[string]int {
	m := make(map[string]int, len(users))
	for _, u := range users {
		m[u.UUID] = u.ID
	}
	return m
}
