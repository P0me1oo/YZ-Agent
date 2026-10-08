package devicegate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	key       sourceKey
	lease     string
	connected time.Time
	sequence  uint64
	revoked   bool
	permits   map[*Permit]struct{}
}

type flight struct {
	sequence uint64
	done     chan struct{}
	err      error
}

type retiredSource struct {
	item     Revocation
	sequence uint64
}

// Manager 持有实际连接的来源引用，只有关闭回调释放后才向面板确认来源离线。
type Manager struct {
	remote  Remote
	enabled atomic.Bool
	mu      sync.Mutex
	syncMu  sync.Mutex
	run     string
	ready   bool
	closing bool
	seq     uint64
	synced  uint64
	ctx     context.Context
	cancel  context.CancelFunc
	sources map[sourceKey]*sourceState
	flights map[sourceKey]*flight
	revoked map[string]time.Time
	retired map[string]retiredSource
	loopWG  sync.WaitGroup
	callWG  sync.WaitGroup
	onError func(error)
}

func New(remote Remote, onError func(error)) *Manager {
	return &Manager{remote: remote, sources: make(map[sourceKey]*sourceState),
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
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
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
	reply, err := m.remote.BeginDeviceSession(ctx, run)
	if err != nil {
		return err
	}
	if reply.Version != 1 || reply.Run != run {
		return fmt.Errorf("设备来源握手结果无效")
	}
	m.mu.Lock()
	m.ready = !m.closing
	m.mu.Unlock()
	return nil
}

// Acquire 仅在某来源从零连接变为有连接时请求面板，已有来源的新连接在本地登记。
func (m *Manager) Acquire(ctx context.Context, userID int, rawIP string) (*Permit, error) {
	if !m.Enabled() || userID <= 0 {
		return nil, nil
	}
	ip := deviceip.PublicAddress(rawIP)
	if ip == "" {
		return nil, nil
	}
	key := sourceKey{user: userID, ip: ip}
	for {
		m.mu.Lock()
		if !m.ready || m.closing || m.ctx.Err() != nil {
			m.mu.Unlock()
			return nil, ErrNotReady
		}
		if source := m.sources[key]; source != nil && len(source.permits) > 0 {
			if !source.revoked {
				p := m.permitLocked(source)
				m.mu.Unlock()
				return p, nil
			}
		}
		if current := m.flights[key]; current != nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-m.ctx.Done():
				return nil, ErrNotReady
			case <-current.done:
				if current.err != nil {
					return nil, current.err
				}
				continue
			}
		}
		m.seq++
		current := &flight{sequence: m.seq, done: make(chan struct{})}
		m.flights[key] = current
		run := m.run
		m.callWG.Add(1)
		m.mu.Unlock()

		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		stop := context.AfterFunc(m.ctx, cancel)
		reply, err := m.request(requestCtx, AdmissionRequest{Run: run, Sequence: current.sequence, UserID: userID, IP: ip})
		stop()
		cancel()
		m.mu.Lock()
		if err == nil && (!m.ready || m.closing || m.run != run || ctx.Err() != nil) {
			err = ErrNotReady
		}
		if err == nil {
			if _, revoked := m.revoked[reply.Lease]; revoked {
				err = ErrRevoked
			}
		}
		var permit *Permit
		if err == nil {
			if old := m.sources[key]; old != nil && old.lease != reply.Lease && len(old.permits) != 0 {
				err = ErrRevoked
			} else {
				source := m.sources[key]
				if source == nil {
					source = &sourceState{key: key, lease: reply.Lease, permits: make(map[*Permit]struct{})}
					m.sources[key] = source
				}
				permit = m.permitLocked(source)
			}
		}
		delete(m.flights, key)
		current.err = err
		close(current.done)
		m.mu.Unlock()
		m.callWG.Done()
		return permit, err
	}
}

func (m *Manager) request(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
	// 先同步本节点刚发生的新连接，避免用上一轮采样给本机来源排序。
	if err := m.syncAtLeast(ctx, request.Sequence); err != nil {
		return AdmissionReply{}, err
	}
	for {
		reply, err := m.remote.AdmitDeviceSource(ctx, request)
		if err != nil {
			return reply, err
		}
		switch reply.Status {
		case "allowed":
			if !validLease(reply.Lease) {
				return reply, fmt.Errorf("设备来源授权结果无效")
			}
			return reply, nil
		case "denied":
			return reply, &DeniedError{Limit: reply.Limit, Observed: reply.Observed, Reason: reply.Reason}
		case "waiting":
			m.ApplyRevocations(reply.Revoked)
			if len(reply.Revoked) > 0 {
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
	return snapshot
}

func (m *Manager) Sync(ctx context.Context) error {
	return m.syncAtLeast(ctx, 0)
}

func (m *Manager) syncAtLeast(ctx context.Context, required uint64) error {
	if !m.Enabled() {
		return nil
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	covered := required != 0 && m.synced >= required
	m.mu.Unlock()
	if covered {
		return nil
	}
	snapshot := m.Snapshot()
	reply, err := m.remote.SyncDeviceSession(ctx, snapshot)
	if errors.Is(err, ErrSessionLost) {
		m.mu.Lock()
		m.ready = false
		m.run = ""
		m.synced = 0
		m.mu.Unlock()
		m.closeAll()
	}
	if err != nil {
		return err
	}
	if reply.Run != snapshot.Run || reply.Sequence != snapshot.Sequence {
		return fmt.Errorf("设备来源快照未被确认")
	}
	m.mu.Lock()
	m.synced = snapshot.Sequence
	for lease, retired := range m.retired {
		if retired.sequence <= snapshot.Sequence {
			delete(m.retired, lease)
		}
	}
	m.mu.Unlock()
	m.ApplyRevocations(reply.Revoked)
	return nil
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
		m.revoked[item.Lease] = now.Add(2 * time.Minute)
		source := m.sources[sourceKey{user: item.UserID, ip: item.IP}]
		if source == nil || source.lease != item.Lease {
			continue
		}
		source.revoked = true
		for permit := range source.permits {
			permits = append(permits, permit)
		}
	}
	m.mu.Unlock()
	for _, permit := range permits {
		permit.revoke()
	}
}

func (m *Manager) closeAll() {
	var permits []*Permit
	m.mu.Lock()
	for _, source := range m.sources {
		for permit := range source.permits {
			permits = append(permits, permit)
		}
	}
	m.mu.Unlock()
	for _, permit := range permits {
		permit.revoke()
	}
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
	defer p.mu.Unlock()
	return !p.closing && !p.released.Load()
}

func (p *Permit) Release() {
	if p == nil || !p.released.CompareAndSwap(false, true) {
		return
	}
	m := p.manager
	m.mu.Lock()
	delete(p.source.permits, p)
	if len(p.source.permits) == 0 && m.sources[p.source.key] == p.source {
		delete(m.sources, p.source.key)
		m.seq++
		m.retired[p.source.lease] = retiredSource{sequence: m.seq, item: Revocation{
			UserID: p.source.key.user, IP: p.source.key.ip, Lease: p.source.lease,
		}}
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
	if p.released.Load() || m.sources[p.source.key] != p.source {
		return false
	}
	m.seq++
	p.source.sequence, p.source.connected = m.seq, time.Now()
	return true
}
