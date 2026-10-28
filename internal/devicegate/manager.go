package devicegate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
)

type sourceKey struct {
	user int
	ip   string
}

type sourceState struct {
	key               sourceKey
	lease             string
	connected         time.Time
	sequence          uint64
	revoked           bool
	permits           map[*Permit]struct{}
	confirmedAt       int64
	expiresAt         int64
	confirmedSequence uint64
	deadline          time.Time
	expiry            *time.Timer
}

type flight struct {
	sequence  uint64
	done      chan struct{}
	err       error
	cancel    context.CancelFunc
	waiters   map[*admissionWaiter]struct{}
	abandoned bool
}

type admissionWaiter struct {
	ctx    context.Context
	permit *Permit
}

type retiredSource struct {
	item     Revocation
	sequence uint64
}

// Manager 持有实际连接的来源引用，只有关闭回调释放后才向面板确认来源离线。
type Manager struct {
	remote       Remote
	enabled      atomic.Bool
	mu           sync.Mutex
	syncMu       chan struct{}
	run          string
	ready        bool
	leaseCache   bool
	users        map[int]bool
	closing      bool
	seq          uint64
	synced       uint64
	confirmed    *Snapshot // 由 syncMu 保护，只保存已经确认的来源内容。
	baseSequence uint64
	ctx          context.Context
	cancel       context.CancelFunc
	sources      map[sourceKey]*sourceState
	flights      map[sourceKey]*flight
	revoked      map[string]time.Time
	retired      map[string]retiredSource
	loopWG       sync.WaitGroup
	callWG       sync.WaitGroup
	onError      func(error)
}

func New(remote Remote, onError func(error)) *Manager {
	return &Manager{remote: remote, syncMu: make(chan struct{}, 1), sources: make(map[sourceKey]*sourceState),
		flights: make(map[sourceKey]*flight), revoked: make(map[string]time.Time), retired: make(map[string]retiredSource), onError: onError}
}

func (m *Manager) Enabled() bool { return m != nil && m.enabled.Load() }

func (m *Manager) Start(ctx context.Context) error {
	if m.remote == nil || !m.remote.DeviceHandoverSupported() {
		return nil
	}
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.enabled.Store(true)
	m.mu.Unlock()
	err := m.begin(ctx)
	m.loopWG.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastError time.Time
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				ready := m.ready
				m.mu.Unlock()
				var err error
				if ready {
					err = m.Sync(m.ctx)
				} else {
					err = m.begin(m.ctx)
				}
				if err != nil && m.onError != nil && time.Since(lastError) >= 30*time.Second {
					m.onError(err)
					lastError = time.Now()
				}
			}
		}
	})
	return err
}

func (m *Manager) begin(ctx context.Context) error {
	if err := m.lockSync(ctx); err != nil {
		return err
	}
	defer m.unlockSync()
	m.mu.Lock()
	if m.closing || len(m.sources) != 0 || len(m.flights) != 0 {
		m.mu.Unlock()
		return ErrNotReady
	}
	if m.run == "" {
		var run [16]byte
		if _, err := rand.Read(run[:]); err != nil {
			m.mu.Unlock()
			return err
		}
		m.run = hex.EncodeToString(run[:])
	}
	run := m.run
	m.mu.Unlock()
	// 新会话不能沿用上一代的续期基线；同步锁保护这两个缓存。
	m.confirmed, m.baseSequence = nil, 0
	reply, err := m.remote.BeginDeviceSession(ctx, run)
	if err != nil {
		return err
	}
	if reply.Version != 1 || reply.Run != run {
		return fmt.Errorf("设备来源握手结果无效")
	}
	m.mu.Lock()
	m.ready = !m.closing
	m.leaseCache = reply.LeaseCache == 1
	m.mu.Unlock()
	return nil
}

// Acquire 优先复用已核验且未到期的许可，账号与节点权限仍由本地用户名单约束。
func (m *Manager) Acquire(ctx context.Context, userID int, rawIP string) (*Permit, error) {
	if !m.Enabled() || userID <= 0 {
		return nil, nil
	}
	ip := deviceip.PublicAddress(rawIP)
	if ip == "" {
		return nil, nil
	}
	key := sourceKey{user: userID, ip: ip}
	// 每个连接独立计算等待时限，重新申请也不能重置已消耗的时间。
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	waiting := false
	for {
		m.mu.Lock()
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return nil, err
		}
		if !m.ready || m.closing || m.ctx.Err() != nil {
			m.mu.Unlock()
			return nil, ErrNotReady
		}
		if m.users != nil && !m.users[userID] {
			m.mu.Unlock()
			return nil, ErrRevoked
		}
		if source := m.sources[key]; source != nil && (m.leaseCache || len(source.permits) > 0) {
			if !source.revoked && (!m.leaseCache || time.Now().Before(source.deadline)) {
				p := m.permitLocked(source)
				m.mu.Unlock()
				return p, nil
			}
		}
		if !waiting {
			// 停止时同时等待连接领取或归还预留引用，不能只等远端任务。
			m.callWG.Add(1)
			defer m.callWG.Done()
			waiting = true
		}
		current := m.flights[key]
		if current == nil {
			m.seq++
			requestCtx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
			current = &flight{sequence: m.seq, done: make(chan struct{}), cancel: cancel,
				waiters: make(map[*admissionWaiter]struct{})}
			m.flights[key] = current
			run := m.run
			m.callWG.Add(1)
			go m.admit(requestCtx, key, run, current)
		}
		waiter := &admissionWaiter{ctx: ctx}
		current.waiters[waiter] = struct{}{}
		m.mu.Unlock()

		select {
		case <-ctx.Done():
		case <-m.ctx.Done():
		case <-current.done:
		}
		m.mu.Lock()
		delete(current.waiters, waiter)
		if len(current.waiters) == 0 && m.flights[key] == current {
			// 全部连接已退出，取消共享任务。新连接等它清理后重新申请，
			// 不与尚未结束的旧序号并行，也不继承它的取消结果。
			current.abandoned = true
			current.cancel()
		}
		permit, err, abandoned := waiter.permit, current.err, current.abandoned
		stopped := m.closing || m.ctx.Err() != nil
		m.mu.Unlock()
		if ctxErr := ctx.Err(); ctxErr != nil {
			permit.Release()
			return nil, ctxErr
		}
		if stopped {
			permit.Release()
			return nil, ErrNotReady
		}
		if abandoned {
			continue
		}
		return permit, err
	}
}

// 共享申请归属于来源，单个连接取消只移除自己的等待和预留授权。
func (m *Manager) admit(ctx context.Context, key sourceKey, run string, current *flight) {
	defer m.callWG.Done()
	defer current.cancel()
	reply, err := m.request(ctx, AdmissionRequest{Run: run, Sequence: current.sequence, UserID: key.user, IP: key.ip})
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		if m.users != nil && !m.users[key.user] {
			err = ErrRevoked
		}
	}
	if err == nil {
		switch {
		case !m.ready || m.closing || m.run != run || m.ctx.Err() != nil:
			err = ErrNotReady
		case ctx.Err() != nil:
			err = ctx.Err()
		case current.abandoned:
			err = context.Canceled
		}
	}
	if err == nil {
		if _, revoked := m.revoked[reply.Lease]; revoked {
			err = ErrRevoked
		} else if old := m.sources[key]; old != nil && old.lease != reply.Lease && len(old.permits) != 0 {
			err = ErrRevoked
		}
	}
	if err == nil && m.leaseCache {
		// 核验返回到分配连接之间可能发生到期或撤销，不能据迟到结果重建空许可。
		source := m.sources[key]
		if source == nil || source.lease != reply.Lease || source.revoked || !time.Now().Before(source.deadline) {
			err = ErrRevoked
		}
	}
	if err == nil {
		for waiter := range current.waiters {
			if waiter.ctx.Err() != nil {
				continue
			}
			source := m.sources[key]
			if source == nil {
				source = &sourceState{key: key, lease: reply.Lease, permits: make(map[*Permit]struct{})}
				m.sources[key] = source
			}
			// 在唤醒前为每个有效连接保留引用，先醒来的短连接释放时
			// 不会让其他仍在领取授权的连接重新请求面板。
			waiter.permit = m.permitLocked(source)
		}
	}
	delete(m.flights, key)
	current.err = err
	close(current.done)
}

func (m *Manager) request(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
	m.mu.Lock()
	cached := m.leaseCache
	m.mu.Unlock()
	if cached {
		return m.requestCached(ctx, request)
	}
	combined := false
	if capability, ok := m.remote.(AdmissionSyncRemote); ok {
		combined = capability.DeviceAdmissionSyncSupported()
	}
	if !combined {
		// 旧面板先同步本节点的新连接，再申请，保留原协议。
		if err := m.syncAtLeast(ctx, request.Sequence); err != nil {
			return AdmissionReply{}, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return AdmissionReply{}, err
		}
		if combined {
			// 每次重试都带当前完整状态，不用旧授权或旧快照放行。
			// 不持有同步锁等待授权，各来源共享任务仍可独立取消。
			snapshot := m.Snapshot()
			request.Snapshot = &snapshot
		}
		reply, err := m.remote.AdmitDeviceSource(ctx, request)
		if combined && errors.Is(err, ErrAdmissionSyncUnsupported) {
			combined, request.Snapshot = false, nil
			// 回滚后的面板必须重新确认当前完整状态，不能复用合并响应覆盖的序号。
			if err := m.Sync(ctx); err != nil {
				return AdmissionReply{}, err
			}
			continue
		}
		if errors.Is(err, ErrSessionLost) {
			m.loseSession(request.Run)
		}
		if err != nil {
			return reply, err
		}
		if err := ctx.Err(); err != nil {
			return AdmissionReply{}, err
		}
		if combined {
			if reply.Sync == nil || reply.Sync.Run != request.Snapshot.Run || reply.Sync.Sequence != request.Snapshot.Sequence {
				return AdmissionReply{}, fmt.Errorf("设备来源申请的完整快照未被确认")
			}
			m.confirmSnapshot(*request.Snapshot, request.UserID)
			m.ApplyRevocations(reply.Sync.Revoked)
		}
		switch reply.Status {
		case "allowed":
			if !validLease(reply.Lease) {
				return reply, fmt.Errorf("设备来源授权结果无效")
			}
			return reply, nil
		case "denied":
			return reply, &DeniedError{Limit: reply.Limit, Observed: reply.Observed, Reason: reply.Reason, Snapshot: reply.LimitSnapshot}
		case "waiting":
			m.ApplyRevocations(reply.Revoked)
			if !combined && len(reply.Revoked) > 0 {
				if err := m.Sync(ctx); err != nil {
					return reply, err
				}
			}
		default:
			return reply, fmt.Errorf("设备来源响应状态无效")
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AdmissionReply{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func validLease(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (m *Manager) permitLocked(source *sourceState) *Permit {
	m.seq++
	source.sequence, source.connected = m.seq, time.Now()
	p := &Permit{manager: m, source: source}
	source.permits[p] = struct{}{}
	return p
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	snapshot := Snapshot{Run: m.run, Sequence: m.seq, Pending: []uint64{}, Sources: []Source{}, Retired: []Revocation{}}
	if m.leaseCache {
		snapshot.LeaseCache = 1
	}
	for _, retired := range m.retired {
		snapshot.Retired = append(snapshot.Retired, retired.item)
	}
	for _, f := range m.flights {
		snapshot.Pending = append(snapshot.Pending, f.sequence)
	}
	for _, s := range m.sources {
		if len(s.permits) > 0 {
			snapshot.Sources = append(snapshot.Sources, Source{UserID: s.key.user, IP: s.key.ip, Lease: s.lease,
				ConnectSequence: s.sequence, AgeMS: max(0, time.Since(s.connected).Milliseconds())})
		}
	}
	sort.Slice(snapshot.Pending, func(i, j int) bool { return snapshot.Pending[i] < snapshot.Pending[j] })
	sort.Slice(snapshot.Sources, func(i, j int) bool { return snapshot.Sources[i].Lease < snapshot.Sources[j].Lease })
	sort.Slice(snapshot.Retired, func(i, j int) bool { return snapshot.Retired[i].Lease < snapshot.Retired[j].Lease })
	return snapshot
}

func (m *Manager) Sync(ctx context.Context) error {
	return m.syncAtLeast(ctx, 0)
}

func (m *Manager) syncAtLeast(ctx context.Context, required uint64) error {
	if !m.Enabled() {
		return nil
	}
	if err := m.lockSync(ctx); err != nil {
		return err
	}
	defer m.unlockSync()
	m.mu.Lock()
	covered := required != 0 && m.synced >= required
	m.mu.Unlock()
	if covered {
		return nil
	}
	snapshot := m.Snapshot()
	wire := snapshot
	if capability, ok := m.remote.(RenewalRemote); ok && capability.DeviceRenewalSupported() && m.baseSequence > 0 && sameSnapshot(m.confirmed, snapshot) {
		wire = Snapshot{Run: snapshot.Run, Sequence: snapshot.Sequence, Unchanged: true, BaseSequence: m.baseSequence}
	}
	wire.LeaseCache = snapshot.LeaseCache
	started := time.Now()
	reply, err := m.remote.SyncDeviceSession(ctx, wire)
	if wire.Unchanged && errors.Is(err, ErrSnapshotRequired) {
		wire = snapshot
		reply, err = m.remote.SyncDeviceSession(ctx, wire)
	}
	if errors.Is(err, ErrSessionLost) {
		m.loseSession(snapshot.Run)
		m.confirmed = nil
		m.baseSequence = 0
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		m.ApplyRevocations(reply.Revoked)
		return err
	}
	if reply.Run != snapshot.Run || reply.Sequence != snapshot.Sequence {
		return fmt.Errorf("设备来源快照未被确认")
	}
	if snapshot.LeaseCache == 1 {
		if reply.LeaseCache != 1 {
			return fmt.Errorf("面板未核验设备缓存许可")
		}
		if err := m.acceptLeases(reply, snapshot.Sequence, started); err != nil {
			return err
		}
	}
	m.confirmed = &snapshot
	if !wire.Unchanged {
		m.baseSequence = snapshot.Sequence
	}
	m.confirmSnapshot(snapshot, 0)
	m.ApplyRevocations(reply.Revoked)
	return nil
}

func (m *Manager) confirmSnapshot(snapshot Snapshot, userID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ready || m.run != snapshot.Run {
		return
	}
	m.synced = max(m.synced, snapshot.Sequence)
	for lease, retired := range m.retired {
		if retired.sequence <= snapshot.Sequence && (userID == 0 || retired.item.UserID == userID) {
			delete(m.retired, lease)
		}
	}
}

func (m *Manager) loseSession(run string) {
	m.mu.Lock()
	if m.run != run {
		m.mu.Unlock()
		return
	}
	m.ready, m.run, m.synced = false, "", 0
	m.mu.Unlock()
	m.closeAll()
}

func (m *Manager) lockSync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.syncMu <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.unlockSync()
			return err
		}
		return nil
	}
}

func (m *Manager) unlockSync() { <-m.syncMu }

func sameSnapshot(previous *Snapshot, current Snapshot) bool {
	return previous != nil && previous.Run == current.Run && slices.Equal(previous.Pending, current.Pending) &&
		slices.Equal(previous.Retired, current.Retired) && slices.EqualFunc(previous.Sources, current.Sources, func(a, b Source) bool {
		// 来源年龄自然增长不算新连接；新连接序号必须参与比较，保持替换顺序。
		a.AgeMS, b.AgeMS = 0, 0
		return a == b
	})
}

func (m *Manager) ApplyRevocations(items []Revocation) {
	var permits []*Permit
	m.mu.Lock()
	now := time.Now()
	for lease, until := range m.revoked {
		if now.After(until) {
			delete(m.revoked, lease)
		}
	}
	for _, item := range items {
		if item.UserID <= 0 || !validLease(item.Lease) {
			continue
		}
		m.revoked[item.Lease] = now.Add(leaseGrace + time.Minute)
		source := m.sources[sourceKey{user: item.UserID, ip: item.IP}]
		if source == nil || source.lease != item.Lease {
			if m.leaseCache {
				m.retireLocked(item)
			}
			continue
		}
		source.revoked = true
		if source.expiry != nil {
			source.expiry.Stop()
		}
		if len(source.permits) == 0 {
			m.removeSourceLocked(source)
		}
		for permit := range source.permits {
			permits = append(permits, permit)
		}
	}
	m.mu.Unlock()
	revokePermits(permits)
}

func (m *Manager) closeAll() {
	var permits []*Permit
	m.mu.Lock()
	for _, source := range m.sources {
		source.revoked = true
		if source.expiry != nil {
			source.expiry.Stop()
		}
		if len(source.permits) == 0 {
			m.removeSourceLocked(source)
		}
		for permit := range source.permits {
			permits = append(permits, permit)
		}
	}
	m.mu.Unlock()
	revokePermits(permits)
}

// 同时发起全部关闭，并保持调用返回前已执行回调的原有约定。
func revokePermits(permits []*Permit) {
	var closing sync.WaitGroup
	for _, permit := range permits {
		closing.Go(permit.revoke)
	}
	closing.Wait()
}

// Stop 在核心停止后调用，最后的空快照确认真实关闭；不会用清空计数代替关闭连接。
func (m *Manager) Stop(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	m.mu.Lock()
	m.closing, m.ready = true, false
	cancel := m.cancel
	m.mu.Unlock()
	cancel()
	m.closeAll()
	m.callWG.Wait()
	m.loopWG.Wait()
	return m.Sync(ctx)
}

type Permit struct {
	manager  *Manager
	source   *sourceState
	mu       sync.Mutex
	closing  bool
	close    func()
	released atomic.Bool
}

// Bind 可先绑定取消拨号，再绑定真实关闭。迟到的绑定同样执行已收到的撤销。
func (p *Permit) Bind(close func()) {
	if p == nil {
		return
	}
	p.mu.Lock()
	closing := p.closing || p.released.Load()
	if !closing {
		p.close = close
	}
	p.mu.Unlock()
	if closing && close != nil {
		close()
	}
}

func (p *Permit) revoke() {
	p.mu.Lock()
	p.closing = true
	close := p.close
	p.close = nil
	p.mu.Unlock()
	if close != nil {
		close()
	}
}

func (p *Permit) Valid() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	valid := !p.closing && !p.released.Load()
	p.mu.Unlock()
	if !valid {
		return false
	}
	m := p.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	return !p.source.revoked && (!m.leaseCache || time.Now().Before(p.source.deadline))
}

func (p *Permit) Release() {
	if p == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	m := p.manager
	m.mu.Lock()
	delete(p.source.permits, p)
	if len(p.source.permits) == 0 && m.sources[p.source.key] == p.source {
		if !m.leaseCache || p.source.revoked || m.closing || !time.Now().Before(p.source.deadline) {
			m.removeSourceLocked(p.source)
		}
	}
	m.mu.Unlock()
}

// NewConnection 用于共享同一授权的隧道，仅新建内部连接时更新时间，收发数据不会续期排序。
func (p *Permit) NewConnection() bool {
	if p == nil {
		return true
	}
	if !p.Valid() {
		return false
	}
	m := p.manager
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.released.Load() || p.source.revoked || m.sources[p.source.key] != p.source || (m.leaseCache && !time.Now().Before(p.source.deadline)) {
		return false
	}
	m.seq++
	p.source.sequence, p.source.connected = m.seq, time.Now()
	return true
}
