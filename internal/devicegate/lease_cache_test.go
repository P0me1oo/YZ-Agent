package devicegate

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type leaseRemote struct {
	testRemote
	leases []Lease
}

func (*leaseRemote) BeginDeviceSession(_ context.Context, run string) (BeginReply, error) {
	return BeginReply{Version: 1, Run: run, LeaseCache: 1}, nil
}

func (r *leaseRemote) AdmitDeviceSource(ctx context.Context, req AdmissionRequest) (AdmissionReply, error) {
	r.calls.Add(1)
	if r.admit != nil {
		return r.admit(ctx, req)
	}
	return AdmissionReply{}, errors.New("测试面板已断联")
}

func (r *leaseRemote) SyncDeviceSession(ctx context.Context, snapshot Snapshot) (SyncReply, error) {
	if r.sync != nil {
		return r.sync(ctx, snapshot)
	}
	return SyncReply{Run: snapshot.Run, Sequence: snapshot.Sequence, LeaseCache: 1,
		ServerTime: time.Now().UnixMilli(), Leases: r.leases}, nil
}

func cachedManager(t *testing.T, remote *leaseRemote) *Manager {
	t.Helper()
	m := New(remote, nil)
	// 不运行周期同步，以精确控制断联和响应顺序；握手、申请、同步仍走真实代码。
	m.ctx, m.cancel = context.WithCancel(t.Context())
	m.enabled.Store(true)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	m.SetUsers([]int{1, 2})
	t.Cleanup(func() {
		m.mu.Lock()
		m.closing = true
		m.mu.Unlock()
		m.cancel()
		m.closeAll()
		m.callWG.Wait()
	})
	return m
}

func testLease(user int, ip string, duration time.Duration) Lease {
	now := time.Now().UnixMilli()
	return Lease{UserID: user, IP: ip, Lease: strings.Repeat(string(rune('a'+user)), 32),
		ConfirmedAt: now, ExpiresAt: now + duration.Milliseconds()}
}

func TestLeaseCacheSyncedIdleLicenseAllowsReconnectWithoutPanel(t *testing.T) {
	remote := &leaseRemote{leases: []Lease{testLease(1, "8.8.8.8", time.Hour)}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, "8.8.8.8")
	deadline := p.source.deadline
	p.Release()
	remote.sync = func(context.Context, Snapshot) (SyncReply, error) { return SyncReply{}, errors.New("面板断联") }
	if err := m.Sync(t.Context()); err == nil {
		t.Fatal("测试必须模拟断联")
	}
	for range 5 {
		p = acquire(t, m, 1, "8.8.8.8")
		if !p.Valid() || !p.source.deadline.Equal(deadline) || !p.NewConnection() || !p.source.deadline.Equal(deadline) {
			t.Fatal("新连接不得移动固定有效期")
		}
		p.Release()
	}
	if remote.calls.Load() != 0 {
		t.Fatal("已有缓存许可不应请求面板")
	}
	if _, err := m.Acquire(t.Context(), 2, "8.8.8.8"); err == nil {
		t.Fatal("另一个账号不能共用许可")
	}
	if _, err := m.Acquire(t.Context(), 1, "1.1.1.1"); err == nil {
		t.Fatal("未知来源断联时不得放行")
	}
}

func TestLeaseCacheExpiryClosesConnectionEvenWhenSyncIsBlocked(t *testing.T) {
	remote := &leaseRemote{leases: []Lease{testLease(1, "8.8.8.8", 150*time.Millisecond)}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, "8.8.8.8")
	closed := make(chan struct{})
	p.Bind(func() { p.Release(); close(closed) })
	started, unblock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	remote.sync = func(context.Context, Snapshot) (SyncReply, error) {
		close(started)
		<-unblock
		return SyncReply{}, errors.New("面板断联")
	}
	go func() { done <- m.Sync(t.Context()) }()
	<-started
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Error("网络同步阻塞推迟了到期关闭")
	}
	close(unblock)
	<-done
	if p.Valid() || p.NewConnection() {
		t.Fatal("到期许可仍然有效")
	}
	if _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); err == nil {
		t.Fatal("到期后不得断联重连")
	}
}

func TestLeaseCacheRepeatedAndOutOfOrderConfirmationsKeepDeadline(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	m := cachedManager(t, &leaseRemote{})
	started := time.Now()
	deadline, err := leaseDeadline(lease, lease.ConfirmedAt, started)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.installLeaseLocked(lease, deadline, 10) || !m.installLeaseLocked(lease, deadline.Add(time.Minute), 11) {
		t.Fatal("同一次确认应保持可用")
	}
	source := m.sources[sourceKey{user: 1, ip: lease.IP}]
	if !source.deadline.Equal(deadline) {
		t.Fatal("重复确认延长了期限")
	}
	newer := lease
	newer.ConfirmedAt++
	newer.ExpiresAt++
	if m.installLeaseLocked(newer, deadline.Add(time.Minute), 9) {
		t.Fatal("乱序状态不能覆盖新确认")
	}
}

func TestLeaseCacheRevocationOverridesRenewalAndLateAdmission(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, lease.IP)
	var closes atomic.Int64
	p.Bind(func() { closes.Add(1); p.Release() })
	revoked := Revocation{UserID: 1, IP: lease.IP, Lease: lease.Lease}
	m.ApplyRevocations([]Revocation{revoked})
	remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		lease.ConfirmedAt++
		lease.ExpiresAt++
		return SyncReply{Run: s.Run, Sequence: s.Sequence, LeaseCache: 1,
			ServerTime: lease.ConfirmedAt, Leases: []Lease{lease}, Revoked: []Revocation{revoked}}, nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		return AdmissionReply{Status: "allowed", Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt,
			ExpiresAt: lease.ExpiresAt, ServerTime: lease.ConfirmedAt,
			Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
	}
	if _, err := m.Acquire(t.Context(), 1, lease.IP); !errors.Is(err, ErrRevoked) {
		t.Fatalf("撤销后迟到授权被接受：%v", err)
	}
	if closes.Load() != 1 {
		t.Fatal("撤销关闭不幂等")
	}
}

func TestLeaseCacheUserRemovalImmediatelyClosesAndDisablesLocalReuse(t *testing.T) {
	remote := &leaseRemote{leases: []Lease{testLease(1, "8.8.8.8", time.Hour)}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, "8.8.8.8")
	var closed atomic.Bool
	p.Bind(func() { closed.Store(true); p.Release() })
	m.SetUsers([]int{2})
	if !closed.Load() || p.Valid() {
		t.Fatal("已知账号移除未立即关闭")
	}
	if _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); !errors.Is(err, ErrRevoked) || remote.calls.Load() != 0 {
		t.Fatalf("已知无权限账号仍请求许可：%v", err)
	}
}

func TestLeaseCacheRestartDoesNotRestoreOfflineLicense(t *testing.T) {
	remote := &leaseRemote{leases: []Lease{testLease(1, "8.8.8.8", time.Hour)}}
	old := cachedManager(t, remote)
	if err := old.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, old, 1, "8.8.8.8")
	p.Release()
	fresh := cachedManager(t, &leaseRemote{})
	if _, err := fresh.Acquire(t.Context(), 1, "8.8.8.8"); err == nil {
		t.Fatal("节点重启不能恢复断联许可")
	}
}

func TestLeaseCacheSlowCloseDoesNotDelayOtherExpiredConnections(t *testing.T) {
	remote := &leaseRemote{leases: []Lease{testLease(1, "8.8.8.8", 150*time.Millisecond)}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := acquire(t, m, 1, "8.8.8.8")
	second := acquire(t, m, 1, "8.8.8.8")
	entered, unblock, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	first.Bind(func() { close(entered); <-unblock; first.Release() })
	second.Bind(func() { second.Release(); close(closed) })
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Error("到期关闭未执行")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Error("单条慢关闭阻塞其他到期连接")
	}
	close(unblock)
}

func TestLeaseCacheSlowAdmissionDoesNotBlockOtherAccountsOrSync(t *testing.T) {
	started, unblock, other := make(chan struct{}), make(chan struct{}), make(chan struct{})
	remote := &leaseRemote{}
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		if req.UserID == 1 {
			close(started)
			<-unblock
		} else {
			close(other)
		}
		lease := testLease(req.UserID, req.IP, time.Hour)
		return AdmissionReply{Status: "allowed", Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt,
			ExpiresAt: lease.ExpiresAt, ServerTime: lease.ConfirmedAt,
			Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
	}
	m := cachedManager(t, remote)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { p, err := m.Acquire(t.Context(), 1, "8.8.8.8"); p.Release(); first <- err }()
	<-started
	go func() { p, err := m.Acquire(t.Context(), 2, "8.8.8.8"); p.Release(); second <- err }()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Error("慢申请阻塞了另一个账号")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	err := m.Sync(ctx)
	cancel()
	if err != nil {
		t.Errorf("慢申请阻塞周期同步：%v", err)
	}
	close(unblock)
	if err := <-first; err != nil {
		t.Error(err)
	}
	if err := <-second; err != nil {
		t.Error(err)
	}
}

func TestLeaseCacheOldEmptySyncCannotRemoveNewlyAdmittedLicense(t *testing.T) {
	remote := &leaseRemote{}
	m := cachedManager(t, remote)
	old := m.Snapshot()
	oldTime := time.Now().UnixMilli()
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		lease := testLease(req.UserID, req.IP, time.Hour)
		return AdmissionReply{Status: "allowed", Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt,
			ExpiresAt: lease.ExpiresAt, ServerTime: lease.ConfirmedAt,
			Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
	}
	p := acquire(t, m, 1, "8.8.8.8")
	if err := m.acceptLeases(SyncReply{Run: old.Run, Sequence: old.Sequence, LeaseCache: 1,
		ServerTime: oldTime}, old.Sequence, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !p.Valid() {
		t.Fatal("迟到的旧空快照撤销了新许可")
	}
}

func TestLeaseCacheSlowRevocationCloseDoesNotDelayOtherConnections(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	m := cachedManager(t, &leaseRemote{leases: []Lease{lease}})
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := acquire(t, m, 1, lease.IP)
	second := acquire(t, m, 1, lease.IP)
	entered, unblock, closed, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	first.Bind(func() { close(entered); <-unblock; first.Release() })
	second.Bind(func() { second.Release(); close(closed) })
	go func() { m.ApplyRevocations([]Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}}); close(done) }()
	<-entered
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Error("单条慢关闭拖延其他已撤销连接")
	}
	if first.Valid() || second.Valid() {
		t.Error("已知撤销仍可使用")
	}
	close(unblock)
	<-done
}

func TestLeaseDeadlineSubtractsResponseDelayAndRejectsInvalidTimes(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	started := time.Now().Add(-10 * time.Second)
	deadline, err := leaseDeadline(lease, lease.ConfirmedAt, started)
	if err != nil || deadline.Sub(started) != time.Hour {
		t.Fatalf("期限必须从请求发出时计算：%v", err)
	}
	for _, invalid := range []Lease{
		{UserID: 1, IP: lease.IP, Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt, ExpiresAt: lease.ExpiresAt + 1},
		{UserID: 1, IP: "127.0.0.1", Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt, ExpiresAt: lease.ExpiresAt},
		{UserID: 1, IP: lease.IP, Lease: lease.Lease, ConfirmedAt: lease.ExpiresAt, ExpiresAt: lease.ExpiresAt},
	} {
		if _, err := leaseDeadline(invalid, lease.ConfirmedAt, started); err == nil {
			t.Fatal("无效期限被接受")
		}
	}
	if _, err := leaseDeadline(lease, lease.ExpiresAt, started); !errors.Is(err, ErrRevoked) {
		t.Fatal("到期响应被接受")
	}
}
