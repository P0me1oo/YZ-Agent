package xray

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
	_ "unsafe"

	xrayDispatcher "github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// Access xray's internal config creator registry so we can replace the
// default dispatcher factory with ours. This runs AFTER xray's init()
// functions because our package imports xray (dependency order guarantee).
//
//go:linkname typeCreatorRegistry github.com/xtls/xray-core/common.typeCreatorRegistry
var typeCreatorRegistry map[reflect.Type]common.ConfigCreator

var origDispatcherFactory common.ConfigCreator

// globalLimitDispatcher is set when the factory creates a LimitDispatcher.
// The Xray kernel reads it to configure limits and get connections.
var globalLimitDispatcher atomic.Pointer[LimitDispatcher]

func init() {
	configType := reflect.TypeOf((*xrayDispatcher.Config)(nil))
	origDispatcherFactory = typeCreatorRegistry[configType]
	typeCreatorRegistry[configType] = limitDispatcherFactory
}

func limitDispatcherFactory(ctx context.Context, config interface{}) (interface{}, error) {
	orig, err := origDispatcherFactory(ctx, config)
	if err != nil {
		return nil, err
	}
	inner, ok := orig.(routing.Dispatcher)
	if !ok {
		return orig, nil
	}
	ld := &LimitDispatcher{
		inner:      orig,
		innerDisp:  inner,
		limitedIPs: make(map[string]map[string]int),
	}
	globalLimitDispatcher.Store(ld)
	nlog.Core().Debug("xray: limit dispatcher installed")
	return ld, nil
}

// LimitDispatcher wraps xray's DefaultDispatcher to enforce per-user
// admission checks before a request is dispatched into xray-core.
//
// It intentionally does NOT mutate transport.Link.Reader/Writer. Xray's
// mux/XUDP close path requires the original concrete *pipe.Reader to remain
// intact, so the dispatcher is limited to gate-keeping and safe connection
// lifecycle bookkeeping.
type LimitDispatcher struct {
	inner     interface{}        // original DefaultDispatcher (Feature + Dispatcher)
	innerDisp routing.Dispatcher // same object, typed as Dispatcher

	// 有设备上限的用户由同一把锁保护，检查和登记在锁内完成。
	mu               sync.RWMutex
	limitedIPs       map[string]map[string]int // email → sourceIP → refcount
	deviceLimits     map[string]int            // email → max devices
	emailToUID       map[string]int            // email → panel user ID
	globalDevices    map[int]map[string]bool   // 面板汇总的来源 IP 快照
	globalLastUpdate time.Time

	// unlimitedIPs: users without device limit — sync.Map for lock-free access.
	// Each entry is *ipCounter{ips sync.Map}.
	unlimitedIPs sync.Map // email → *ipCounter

	connCount atomic.Int64 // total active connections tracked by dispatcher

	// userConns 按面板 userID 统计活跃连接数，供并发上限判断。
	// xray 的 email 是 user@<id>，只有装了 connLimiter 才会开始计数。
	userConns sync.Map // int → *atomic.Int64

	// connLimiter 做连接数和新建速率准入，nil 表示不限制。
	connLimiter atomic.Pointer[model.ConnLimiter]
}

// ipCounter tracks IPs for unlimited users without any lock.
type ipCounter struct {
	ips sync.Map // sourceIP → *atomic.Int64 (refcount)
}

// aliveIPs returns a snapshot of distinct IPs.
func (ic *ipCounter) aliveIPs() map[string]bool {
	result := make(map[string]bool)
	ic.ips.Range(func(key, _ interface{}) bool {
		if rv, ok := ic.ips.Load(key); ok && rv.(*atomic.Int64).Load() > 0 {
			result[key.(string)] = true
		}
		return true
	})
	return result
}

// ─── routing.Dispatcher ──────────────────────────────────────────────────────

func (d *LimitDispatcher) Dispatch(ctx context.Context, dest net.Destination) (*transport.Link, error) {
	email, sourceIP, uid, isTCP, err := d.identifyAndCheck(ctx, dest)
	if err != nil {
		return nil, err
	}

	link, err := d.innerDisp.Dispatch(ctx, dest)
	if err != nil {
		if uid > 0 {
			d.userConnCounter(uid).Add(-1)
		}
		if email != "" {
			d.delConn(email, sourceIP)
		}
		return nil, err
	}

	if email != "" {
		d.trackLink(link, email, sourceIP, uid, isTCP)
	}
	return link, nil
}

func (d *LimitDispatcher) DispatchLink(ctx context.Context, dest net.Destination, link *transport.Link) error {
	email, sourceIP, uid, isTCP, err := d.identifyAndCheck(ctx, dest)
	if err != nil {
		return err
	}

	var release func()
	if email != "" {
		release = d.trackLink(link, email, sourceIP, uid, isTCP)
	}
	err = d.innerDisp.DispatchLink(ctx, dest, link)
	if err != nil && release != nil {
		// 内层可能已经关闭 writer；与关闭回调共用一次性回收，避免重复扣减。
		release()
	}
	return err
}

// identifyAndCheck extracts user identity from the session context, enforces
// device and connection limits, and returns the user's email, source IP,
// panel user ID, and TCP flag.
// Returns a non-nil error only when the connection should be rejected.
func (d *LimitDispatcher) identifyAndCheck(ctx context.Context, dest net.Destination) (email, sourceIP string, uid int, isTCP bool, err error) {
	si := session.InboundFromContext(ctx)
	if si == nil || si.User == nil || len(si.User.Email) == 0 {
		return "", "", 0, false, nil
	}
	email = si.User.Email
	sourceIP = si.Source.Address.IP().String()
	isTCP = dest.Network == net.Network_TCP

	// 超限拒绝属于运营需要看到的事件，用 Info 级别，与 sing-box 侧保持一致；
	// 默认日志级别是 Info，用 Debug 会导致节点上完全看不到超限记录。
	if d.checkDeviceLimit(email, sourceIP, isTCP) {
		nlog.Core().Info("xray: device limit exceeded", "email", email, "ip", sourceIP)
		return "", "", 0, false, errors.New("device limit exceeded for " + email)
	}

	uid, kind, limit, observed, reject := d.checkConnGate(email)
	if reject {
		nlog.Core().Info("xray: conn limit exceeded",
			"email", email, "ip", sourceIP, "kind", kind, "limit", limit, "observed", observed)
		// 设备检查已经登记了来源，拒绝前必须回退。
		d.delConn(email, sourceIP)
		return "", "", 0, false, errors.New("connection limit exceeded for " + email)
	}
	return email, sourceIP, uid, isTCP, nil
}

// checkConnGate 判断新连接是否超过该用户的并发或新建速率上限，
// 返回 (userID, 原因, 上限, 实测值, 是否拒绝)。
//
// 没装 connLimiter 时走无锁快速路径，不解析 userID 也不计数。
// 并发检查在速率检查之前，避免并发已超限时还白白消耗一个速率令牌。
// 放行时已占用并发名额，由调度失败分支或连接关闭回调归还。
func (d *LimitDispatcher) checkConnGate(email string) (int, string, int, int, bool) {
	ptr := d.connLimiter.Load()
	if ptr == nil {
		return 0, "", 0, 0, false
	}
	cl := *ptr
	if cl == nil {
		return 0, "", 0, 0, false
	}

	d.mu.RLock()
	uid := d.emailToUID[email]
	d.mu.RUnlock()
	if uid <= 0 {
		return 0, "", 0, 0, false
	}

	counter := d.userConnCounter(uid)
	limit, limited := cl.MaxConnByUserID(uid)
	for {
		current := counter.Load()
		if limited && current >= int64(limit) {
			cl.ReportLimited(uid, model.ConnLimitKindConcurrent, limit, int(current))
			return uid, model.ConnLimitKindConcurrent, limit, int(current), true
		}
		if counter.CompareAndSwap(current, current+1) {
			break
		}
	}

	if !cl.AllowNewConn(uid) {
		counter.Add(-1)
		// 速率上限由 limiter 自己补全，内核不需要知道具体配置。
		cl.ReportLimited(uid, model.ConnLimitKindRate, 0, 0)
		return uid, model.ConnLimitKindRate, 0, 0, true
	}

	return uid, "", 0, 0, false
}

// reportDeviceLimited 把设备数超限记入本周期统计，随状态上报面板。
// 调用方持有 d.mu；limiter 不会回调 dispatcher，不存在锁顺序问题。
func (d *LimitDispatcher) reportDeviceLimited(uid, limit, observed int, sourceIP string) {
	if uid <= 0 {
		return
	}
	ptr := d.connLimiter.Load()
	if ptr == nil {
		return
	}
	if reporter, ok := (*ptr).(model.DeviceLimitReporter); ok {
		reporter.ReportDeviceLimited(uid, limit, observed, sourceIP)
	}
}

// userConnCounter 返回该用户的活跃连接计数器，不存在时创建。
func (d *LimitDispatcher) userConnCounter(uid int) *atomic.Int64 {
	if v, ok := d.userConns.Load(uid); ok {
		return v.(*atomic.Int64)
	}
	v, _ := d.userConns.LoadOrStore(uid, &atomic.Int64{})
	return v.(*atomic.Int64)
}

// trackLink records connection lifecycle without mutating xray-core owned
// transport primitives. This keeps mux/XUDP compatible while still allowing
// the dispatcher to release device-limit state when the link closes.
func (d *LimitDispatcher) trackLink(link *transport.Link, email, sourceIP string, uid int, isTCP bool) func() {
	d.connCount.Add(1)

	// 计数器在这里取一次，关闭回调直接复用，避免关闭路径再查 sync.Map。
	var userConns *atomic.Int64
	if uid > 0 {
		userConns = d.userConnCounter(uid)
	}

	onClose := func() {
		d.delConn(email, sourceIP)
		if userConns != nil {
			userConns.Add(-1)
		}
		d.connCount.Add(-1)
	}

	writer := &closeTrackingWriter{
		Writer:  link.Writer,
		onClose: onClose,
	}
	link.Writer = writer
	return writer.release
}

// ─── features.Feature (delegated) ───────────────────────────────────────────

func (d *LimitDispatcher) Type() interface{} { return routing.DispatcherType() }

func (d *LimitDispatcher) Start() error {
	if s, ok := d.inner.(interface{ Start() error }); ok {
		return s.Start()
	}
	return nil
}

func (d *LimitDispatcher) Close() error {
	if c, ok := d.inner.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

// ─── Limit management (called by Xray kernel) ──────────────────────────────

func (d *LimitDispatcher) UpdateLimits(emailToUID map[string]int, deviceLimits, _ map[string]int) {
	d.mu.Lock()
	d.emailToUID = emailToUID
	d.deviceLimits = deviceLimits
	d.mu.Unlock()

}

// UpdateGlobalDevices 用面板的全量快照更新跨节点设备状态。
func (d *LimitDispatcher) UpdateGlobalDevices(users map[int][]string, updatedAt time.Time) {
	devices := make(map[int]map[string]bool, len(users))
	for uid, ips := range users {
		set := make(map[string]bool, len(ips))
		for _, ip := range ips {
			if public := deviceip.Normalize(ip); public != "" {
				set[public] = true
			}
		}
		devices[uid] = set
	}
	d.mu.Lock()
	d.globalDevices = devices
	d.globalLastUpdate = updatedAt
	d.mu.Unlock()
}

// SetConnLimiter 配置连接数与新建速率准入，传 nil 表示关闭。
func (d *LimitDispatcher) SetConnLimiter(limiter model.ConnLimiter) {
	if limiter == nil {
		d.connLimiter.Store(nil)
		return
	}
	d.connLimiter.Store(&limiter)
}

func (d *LimitDispatcher) ResetConns() {
	d.mu.Lock()
	d.limitedIPs = make(map[string]map[string]int)
	d.mu.Unlock()

	// Clear unlimited IPs
	d.unlimitedIPs.Range(func(key, _ interface{}) bool {
		d.unlimitedIPs.Delete(key)
		return true
	})

	d.userConns.Range(func(key, _ interface{}) bool {
		d.userConns.Delete(key)
		return true
	})

	d.connCount.Store(0)
}

// GetConnectionState returns dispatcher-tracked alive IPs and connection count.
// Traffic bytes are intentionally left to xray's built-in stats pipeline.
func (d *LimitDispatcher) GetConnectionState() (aliveIPs map[int]map[string]bool, connCount int) {
	d.mu.RLock()
	emailToUID := d.emailToUID
	limitedIPs := d.limitedIPs

	aliveIPs = make(map[int]map[string]bool)

	// 持有读锁直到嵌套 map 复制完成，连接增删会同时修改外层和内层 map。
	for email, ipsMap := range limitedIPs {
		uid := emailToUID[email]
		if uid == 0 {
			continue
		}
		ipSet := make(map[string]bool, len(ipsMap))
		for ip := range ipsMap {
			ipSet[ip] = true
		}
		if len(ipSet) > 0 {
			aliveIPs[uid] = ipSet
		}
	}
	d.mu.RUnlock()

	// Collect IPs from unlimited users (lock-free).
	d.unlimitedIPs.Range(func(key, value interface{}) bool {
		email := key.(string)
		uid := emailToUID[email]
		if uid == 0 {
			return true
		}
		ic := value.(*ipCounter)
		if ips := ic.aliveIPs(); len(ips) > 0 {
			// Merge with limited IPs if any
			if existing, ok := aliveIPs[uid]; ok {
				for ip := range ips {
					existing[ip] = true
				}
			} else {
				aliveIPs[uid] = ips
			}
		}
		return true
	})

	connCount = int(d.connCount.Load())
	return
}

// ─── Internal helpers ───────────────────────────────────────────────────────

// checkDeviceLimit enforces per-user device limits.
// Fast path: unlimited users use lock-free sync.Map.
// 有上限的用户在同一把锁内完成检查和登记。
func (d *LimitDispatcher) checkDeviceLimit(email, sourceIP string, _ bool) bool {
	if !deviceip.Public(sourceIP) {
		return false
	}
	d.mu.RLock()
	limit, hasLimit := d.deviceLimits[email]
	d.mu.RUnlock()

	// Fast path: no device limit — use lock-free sync.Map.
	if !hasLimit || limit <= 0 {
		v, _ := d.unlimitedIPs.LoadOrStore(email, &ipCounter{})
		ic := v.(*ipCounter)
		rv, _ := ic.ips.LoadOrStore(sourceIP, &atomic.Int64{})
		rv.(*atomic.Int64).Add(1)
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	ips := d.limitedIPs[email]
	if ips == nil {
		ips = make(map[string]int)
		d.limitedIPs[email] = ips
	}
	if ips[sourceIP] > 0 {
		ips[sourceIP]++
		return false
	}
	uid := d.emailToUID[email]
	fresh := !d.globalLastUpdate.IsZero() && time.Since(d.globalLastUpdate) <= 2*time.Minute
	if fresh && d.globalDevices[uid][sourceIP] {
		ips[sourceIP]++
		return false
	}
	seen := make(map[string]bool, len(ips))
	for ip := range ips {
		seen[ip] = true
	}
	if fresh {
		for ip := range d.globalDevices[uid] {
			seen[ip] = true
		}
	}
	if len(seen) >= limit {
		d.reportDeviceLimited(uid, limit, len(seen), sourceIP)
		return true
	}
	ips[sourceIP]++
	return false
}

// delConn decrements the IP refcount when a connection closes.
func (d *LimitDispatcher) delConn(email, sourceIP string) {
	if !deviceip.Public(sourceIP) {
		return
	}
	// Check if this is an unlimited user first (lock-free).
	if v, ok := d.unlimitedIPs.Load(email); ok {
		ic := v.(*ipCounter)
		if rv, ok := ic.ips.Load(sourceIP); ok {
			counter := rv.(*atomic.Int64)
			if counter.Add(-1) <= 0 {
				ic.ips.Delete(sourceIP)
			}
		}
		return
	}

	// Limited user — use write lock.
	d.mu.Lock()
	defer d.mu.Unlock()
	if ips, ok := d.limitedIPs[email]; ok {
		ips[sourceIP]--
		if ips[sourceIP] <= 0 {
			delete(ips, sourceIP)
		}
		if len(ips) == 0 {
			delete(d.limitedIPs, email)
		}
	}
}

type closeTrackingWriter struct {
	buf.Writer
	onClose func()
	closed  atomic.Bool
}

func (w *closeTrackingWriter) release() {
	if w.closed.CompareAndSwap(false, true) {
		w.onClose()
	}
}

func (w *closeTrackingWriter) Close() error {
	w.release()
	return common.Close(w.Writer)
}

func (w *closeTrackingWriter) Interrupt() {
	w.release()
	common.Interrupt(w.Writer)
}
