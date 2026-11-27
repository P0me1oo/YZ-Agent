package directwg

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"golang.org/x/time/rate"
)

// Managed 在普通 WG 节点上管理用户隧道，其余协议原样交给现有核心。
type Managed struct {
	kernel.Kernel
	mu      sync.Mutex
	state   *State
	runtime *Runtime
	direct  bool
	users   []model.UserSpec
}

func Wrap(base kernel.Kernel) kernel.Kernel {
	m := &Managed{Kernel: base, state: newState()}
	if reader, ok := base.(kernel.UsageReader); ok {
		return &usageManaged{Managed: m, reader: reader}
	}
	return m
}

func (m *Managed) RetireTraffic(candidates model.TrafficCounters) model.TrafficCounters {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 两份账本共享合并后的采样基线，不能只回收其中一份。
	// Managed 锁排除切换，状态锁保留 WG 写入与回收之间的顺序；内核回收不执行网络 I/O。
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if len(m.state.totals) == 0 && len(m.state.users) == 0 {
		if retire, ok := m.Kernel.(kernel.TrafficRetirer); ok {
			return retire.RetireTraffic(candidates)
		}
		return model.TrafficCounters{}
	}
	base, _, _, err := m.Kernel.GetUserTraffic(context.Background())
	if err != nil {
		return model.TrafficCounters{}
	}
	eligible := make(map[int][2]int64)
	parts := model.TrafficCounters{
		Users: make(map[int][2]int64), Relay: candidates.Relay, RelayUsers: candidates.RelayUsers,
	}
	for id, value := range candidates.Users {
		wg := m.state.totals[id]
		current := [2]int64{base[id][0] + wg[0], base[id][1] + wg[1]}
		if current == value && m.state.users[id] == nil {
			eligible[id] = value
			parts.Users[id] = base[id]
		}
	}
	out := model.TrafficCounters{Users: make(map[int][2]int64)}
	if retire, ok := m.Kernel.(kernel.TrafficRetirer); ok {
		out = retire.RetireTraffic(parts)
		if out.Users == nil {
			out.Users = make(map[int][2]int64)
		}
	}
	for id, value := range eligible {
		_, removed := out.Users[id]
		if base[id] != [2]int64{} && !removed {
			continue
		}
		delete(m.state.totals, id)
		out.Users[id] = value
	}
	return out
}
func (m *Managed) Start(n *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.IsDirectWireGuard() {
		if err := model.ValidateDirectWireGuard(n); err != nil {
			return err
		}
		if err := validateUsers(users, n.WireGuard.Address); err != nil {
			return err
		}
	}
	if m.runtime != nil {
		m.runtime.Close()
		m.runtime = nil
	}
	m.direct = n.IsDirectWireGuard()
	if !m.direct {
		return m.Kernel.Start(n, users, tls)
	}
	router, ok := m.Kernel.(Router)
	if !ok {
		return fmt.Errorf("核心未提供普通 WG 路由接口")
	}
	if err := m.Kernel.Start(n, nil, tls); err != nil {
		return err
	}
	runtime, err := start(n, users, m.state, router)
	if err != nil {
		m.Kernel.Stop()
		return err
	}
	m.runtime, m.users = runtime, cloneUsers(users)
	return nil
}
func (m *Managed) Reload(n *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	m.mu.Lock()
	direct := m.direct
	m.mu.Unlock()
	if direct || n.IsDirectWireGuard() {
		return m.Start(n, users, tls)
	}
	return m.Kernel.Reload(n, users, tls)
}
func (m *Managed) Stop() {
	m.stop(false)
}
func (m *Managed) StopNow() { m.stop(true) }
func (m *Managed) stop(immediate bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtime != nil {
		m.runtime.Close()
		m.runtime = nil
	}
	if stopper, ok := m.Kernel.(kernel.ImmediateStopper); ok && immediate {
		stopper.StopNow()
	} else {
		m.Kernel.Stop()
	}
}
func (m *Managed) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.direct {
		return m.runtime != nil && m.runtime.ctx.Err() == nil && m.Kernel.IsRunning()
	}
	return m.Kernel.IsRunning()
}
func (m *Managed) Capabilities() kernel.Capabilities {
	c := m.Kernel.Capabilities()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.direct {
		c.ForceCloseConnection = false
		c.BuiltInTrafficStats = false
	}
	return c
}
func (m *Managed) UpdateUsers(users []model.UserSpec) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.direct {
		return m.Kernel.UpdateUsers(users)
	}
	return m.updateLocked(users)
}
func (m *Managed) updateLocked(users []model.UserSpec) (int, int, error) {
	if m.runtime == nil {
		return 0, 0, fmt.Errorf("WireGuard 未运行")
	}
	add, remove := kernel.UserDiff(m.users, users)
	if err := m.runtime.updateUsers(users); err != nil {
		return 0, 0, err
	}
	m.users = cloneUsers(users)
	return len(add), len(remove), nil
}
func (m *Managed) AddUsers(users []model.UserSpec) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.direct {
		return m.Kernel.AddUsers(users)
	}
	byID := make(map[int]model.UserSpec)
	for _, u := range m.users {
		byID[u.ID] = u
	}
	for _, u := range users {
		byID[u.ID] = u
	}
	next := make([]model.UserSpec, 0, len(byID))
	for _, u := range byID {
		next = append(next, u)
	}
	n, _, err := m.updateLocked(next)
	return n, err
}
func (m *Managed) RemoveUsers(users []model.UserSpec) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.direct {
		return m.Kernel.RemoveUsers(users)
	}
	remove := make(map[int]bool)
	for _, u := range users {
		remove[u.ID] = true
	}
	next := make([]model.UserSpec, 0, len(m.users))
	for _, u := range m.users {
		if !remove[u.ID] {
			next = append(next, u)
		}
	}
	_, n, err := m.updateLocked(next)
	return n, err
}
func (m *Managed) GetUserTraffic(ctx context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	traffic, alive, count, err := m.Kernel.GetUserTraffic(ctx)
	if err != nil {
		return nil, nil, 0, err
	}
	wgTraffic, wgAlive, wgCount := m.state.snapshot()
	if traffic == nil {
		traffic = make(map[int][2]int64, len(wgTraffic))
	}
	for id, value := range wgTraffic {
		current := traffic[id]
		traffic[id] = [2]int64{current[0] + value[0], current[1] + value[1]}
	}
	if m.direct {
		alive, count = wgAlive, wgCount
	}
	return traffic, alive, count, nil
}
func (m *Managed) CloseUserConnections(ctx context.Context, uuid string) error {
	m.mu.Lock()
	direct := m.direct
	m.mu.Unlock()
	if !direct {
		return m.Kernel.CloseUserConnections(ctx, uuid)
	}
	m.state.closeUser(uuid)
	return nil
}
func (m *Managed) SetSpeedLimitFunc(fn func(string) *rate.Limiter) {
	m.state.mu.Lock()
	m.state.speed = fn
	m.state.mu.Unlock()
	// 普通 WG 的限速由外层处理，底层路由没有用户身份；关闭限速时也不给核心传入空函数。
	m.Kernel.SetSpeedLimitFunc(func(uuid string) *rate.Limiter {
		if fn == nil || uuid == "" {
			return nil
		}
		return fn(uuid)
	})
}
func (m *Managed) SetConnLimiter(l model.ConnLimiter) {
	m.state.mu.Lock()
	m.state.connLimiter = l
	m.state.mu.Unlock()
	m.Kernel.SetConnLimiter(l)
}

func (m *Managed) SetDeviceGate(gate *devicegate.Manager) {
	m.state.mu.Lock()
	m.state.deviceGate = gate
	m.state.mu.Unlock()
	if consumer, ok := m.Kernel.(kernel.DeviceGateConsumer); ok {
		consumer.SetDeviceGate(gate)
	}
}
func (m *Managed) UpdateGlobalDevices(users map[int][]string) {
	copyUsers := make(map[int][]string, len(users))
	for id, ips := range users {
		copyUsers[id] = append([]string(nil), ips...)
	}
	m.state.mu.Lock()
	m.state.global = copyUsers
	m.state.globalAt = time.Now()
	m.state.mu.Unlock()
	m.Kernel.UpdateGlobalDevices(users)
}
func (m *Managed) ClearGlobalDevices() {
	m.state.mu.Lock()
	m.state.global = nil
	m.state.globalAt = time.Time{}
	m.state.mu.Unlock()
	m.Kernel.ClearGlobalDevices()
}
func (m *Managed) SetDeviceIPExclude(entries []string) ([]string, bool) {
	m.state.mu.Lock()
	normalized, changed := m.state.filter.SetExcluded(entries)
	m.state.mu.Unlock()
	if v, ok := m.Kernel.(interface {
		SetDeviceIPExclude([]string) ([]string, bool)
	}); ok {
		v.SetDeviceIPExclude(entries)
	}
	return normalized, changed
}
func (m *Managed) NeedsStableSpeedLimiter() bool {
	v, ok := m.Kernel.(kernel.StableSpeedLimiterConsumer)
	return ok && v.NeedsStableSpeedLimiter()
}
func (m *Managed) RefreshSpeedLimits() {
	if v, ok := m.Kernel.(kernel.SpeedLimitRefresher); ok {
		v.RefreshSpeedLimits()
	}
}
func (m *Managed) GetRelayTraffic(ctx context.Context) (map[int][2]int64, error) {
	if v, ok := m.Kernel.(kernel.RelayTrafficReader); ok {
		return v.GetRelayTraffic(ctx)
	}
	return nil, nil
}
func (m *Managed) GetRelayUserTraffic(ctx context.Context) (map[int]map[int][2]int64, error) {
	if v, ok := m.Kernel.(kernel.RelayUserTrafficReader); ok {
		return v.GetRelayUserTraffic(ctx)
	}
	return nil, nil
}
func (m *Managed) GetRelayUserAlive(ctx context.Context) (map[int]map[int]map[string]bool, error) {
	if v, ok := m.Kernel.(kernel.RelayUserAliveReader); ok {
		return v.GetRelayUserAlive(ctx)
	}
	return nil, nil
}
func (m *Managed) GetConnectionSnapshot(ctx context.Context) (map[int]int, map[int]map[int]int, error) {
	m.mu.Lock()
	direct := m.direct
	m.mu.Unlock()
	if !direct {
		if v, ok := m.Kernel.(kernel.ConnectionSnapshotReader); ok {
			return v.GetConnectionSnapshot(ctx)
		}
		return nil, nil, nil
	}
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	counts := make(map[int]int)
	for id, u := range m.state.users {
		counts[id] = len(u.connections)
	}
	return counts, nil, nil
}

func cloneUsers(users []model.UserSpec) []model.UserSpec {
	copyUsers := append([]model.UserSpec(nil), users...)
	for i := range copyUsers {
		copyUsers[i].WireGuard = model.CloneWireGuardPeer(copyUsers[i].WireGuard)
	}
	return copyUsers
}

// ProbeDial 透传诊断请求，保持包装后的双核心检查能力。
func (m *Managed) ProbeDial(ctx context.Context, tag, network, address string) (net.Conn, error) {
	m.mu.Lock()
	base := m.Kernel
	m.mu.Unlock()
	if probe, ok := base.(interface {
		ProbeDial(context.Context, string, string, string) (net.Conn, error)
	}); ok {
		return probe.ProbeDial(ctx, tag, network, address)
	}
	return nil, fmt.Errorf("核心不支持链路检查")
}

// 来源跟随每条转发连接，不用最近一次握手地址代替已有连接的来源。
func (m *Managed) GetIPConnectionSnapshot(ctx context.Context) (map[int]map[string]int, error) {
	m.mu.Lock()
	direct := m.direct
	m.mu.Unlock()
	if !direct {
		if reader, ok := m.Kernel.(kernel.IPConnectionSnapshotReader); ok {
			return reader.GetIPConnectionSnapshot(ctx)
		}
		return nil, nil
	}
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	counts := make(map[int]map[string]int)
	for id, user := range m.state.users {
		for conn := range user.connections {
			if ip := deviceip.PublicAddress(user.connectionIPs[conn]); ip != "" {
				if counts[id] == nil {
					counts[id] = make(map[string]int)
				}
				counts[id][ip]++
			}
		}
	}
	return counts, nil
}

var _ kernel.IPConnectionSnapshotReader = (*Managed)(nil)
