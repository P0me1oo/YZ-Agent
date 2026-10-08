package devicegate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testRemote struct {
	calls atomic.Int64
	admit func(context.Context, AdmissionRequest) (AdmissionReply, error)
	sync  func(context.Context, Snapshot) (SyncReply, error)
}

func (*testRemote) DeviceHandoverSupported() bool { return true }
func (*testRemote) BeginDeviceSession(_ context.Context, run string) (BeginReply, error) {
	return BeginReply{Version: 1, Run: run}, nil
}
func (r *testRemote) AdmitDeviceSource(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
	n := r.calls.Add(1)
	if r.admit != nil {
		return r.admit(ctx, request)
	}
	return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", n)}, nil
}
func (r *testRemote) SyncDeviceSession(ctx context.Context, s Snapshot) (SyncReply, error) {
	if r.sync != nil {
		return r.sync(ctx, s)
	}
	return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
}

func testManager(t *testing.T, r *testRemote) *Manager {
	t.Helper()
	m := New(r, nil)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Stop(ctx)
	})
	return m
}

func acquire(t *testing.T, m *Manager, user int, ip string) *Permit {
	t.Helper()
	p, err := m.Acquire(t.Context(), user, ip)
	if err != nil || p == nil {
		t.Fatalf("来源准入失败：%v", err)
	}
	t.Cleanup(p.Release)
	return p
}

func TestRevocationWaitsForCloseAndIsScopedToAccountAndLease(t *testing.T) {
	r := &testRemote{}
	m := testManager(t, r)
	a := acquire(t, m, 1, "8.8.8.8")
	b := acquire(t, m, 1, "8.8.8.8")
	other := acquire(t, m, 2, "8.8.8.8")
	if r.calls.Load() != 2 {
		t.Fatal("已有来源的新连接不应再次请求面板")
	}
	var closed atomic.Int64
	a.Bind(func() { closed.Add(1) })
	b.Bind(func() { closed.Add(1) })
	other.Bind(func() { t.Error("误关了共用出口的另一个账号") })
	item := Revocation{UserID: 1, IP: "8.8.8.8", Lease: a.source.lease}
	m.ApplyRevocations([]Revocation{item, item})
	if closed.Load() != 2 || len(m.Snapshot().Sources) != 2 {
		t.Fatal("关闭请求应幂等，回调尚未释放时不能提前清空来源")
	}
	a.Release()
	if len(m.Snapshot().Sources) != 2 {
		t.Fatal("同一来源还有一条连接，不能提前确认关闭")
	}
	b.Release()
	s := m.Snapshot()
	if len(s.Sources) != 1 || s.Sources[0].UserID != 2 || !other.Valid() {
		t.Fatal("关闭结果未按账号隔离")
	}
}

func TestLateGrantCannotResurrectRevokedSource(t *testing.T) {
	started, proceed := make(chan AdmissionRequest, 1), make(chan struct{})
	lease := strings.Repeat("a", 32)
	r := &testRemote{admit: func(ctx context.Context, request AdmissionRequest) (AdmissionReply, error) {
		started <- request
		select {
		case <-proceed:
			return AdmissionReply{Status: "allowed", Lease: lease}, nil
		case <-ctx.Done():
			return AdmissionReply{}, ctx.Err()
		}
	}}
	m := testManager(t, r)
	done := make(chan error, 1)
	go func() { _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); done <- err }()
	request := <-started
	s := m.Snapshot()
	if len(s.Pending) != 1 || s.Pending[0] != request.Sequence || len(s.Sources) != 0 {
		t.Fatal("尚未完成的授权必须在快照中保留，不能误认为已关闭")
	}
	m.ApplyRevocations([]Revocation{{UserID: 1, IP: "8.8.8.8", Lease: lease}})
	close(proceed)
	if err := <-done; !errors.Is(err, ErrRevoked) {
		t.Fatalf("迟到的旧授权应拒绝，得到 %v", err)
	}
	s = m.Snapshot()
	if len(s.Pending) != 0 || len(s.Sources) != 0 {
		t.Fatal("被撤销的迟到请求遗留了来源")
	}
}

func TestRevocationBeforeConnectionBindingClosesLateConnection(t *testing.T) {
	m := testManager(t, &testRemote{})
	p := acquire(t, m, 1, "8.8.8.8")
	m.ApplyRevocations([]Revocation{{UserID: 1, IP: "8.8.8.8", Lease: p.source.lease}})
	if len(m.Snapshot().Sources) != 1 {
		t.Fatal("拨号尚未结束，不能伪造关闭确认")
	}
	closed := false
	p.Bind(func() { closed = true; p.Release() })
	if !closed || p.Valid() || len(m.Snapshot().Sources) != 0 {
		t.Fatal("迟到的连接必须立即关闭并释放")
	}
}

func TestStaleRevocationDoesNotCloseReturnedSource(t *testing.T) {
	m := testManager(t, &testRemote{})
	old := acquire(t, m, 1, "8.8.8.8")
	item := Revocation{UserID: 1, IP: "8.8.8.8", Lease: old.source.lease}
	old.Release()
	current := acquire(t, m, 1, "8.8.8.8")
	current.Bind(func() { t.Error("迟到的撤销误关了重新获准的来源") })
	m.ApplyRevocations([]Revocation{item})
	if !current.Valid() || len(m.Snapshot().Sources) != 1 || current.source.lease == item.Lease {
		t.Fatal("重新接入必须使用独立授权")
	}
}

func TestSnapshotAndTrafficDoNotChangeLastNewConnection(t *testing.T) {
	m := testManager(t, &testRemote{})
	p := acquire(t, m, 1, "8.8.8.8")
	first := m.Snapshot().Sources[0]
	second := m.Snapshot().Sources[0]
	if first.ConnectSequence != second.ConnectSequence {
		t.Fatal("重复上报不能让长连接变成最近新建")
	}
	if !p.NewConnection() || m.Snapshot().Sources[0].ConnectSequence <= first.ConnectSequence {
		t.Fatal("真正新建连接后必须更新排序依据")
	}
}

func TestDeniedAndMalformedAdmissionsNeverCreateSource(t *testing.T) {
	for _, reply := range []AdmissionReply{
		{Status: "denied", Limit: 2, Observed: 2, Reason: "cooldown"},
		{Status: "allowed", Lease: "invalid"},
		{Status: "unexpected"},
	} {
		t.Run(reply.Status, func(t *testing.T) {
			r := &testRemote{admit: func(context.Context, AdmissionRequest) (AdmissionReply, error) { return reply, nil }}
			m := testManager(t, r)
			if p, err := m.Acquire(t.Context(), 1, "8.8.8.8"); p != nil || err == nil {
				t.Fatal("失败授权不应创建来源")
			}
			if len(m.Snapshot().Sources) != 0 || len(m.Snapshot().Pending) != 0 {
				t.Fatal("失败请求遗留了名额")
			}
		})
	}
}

func TestSessionLossClosesExistingConnectionsBeforeReinitializing(t *testing.T) {
	var lost atomic.Bool
	r := &testRemote{sync: func(_ context.Context, s Snapshot) (SyncReply, error) {
		if lost.Load() {
			return SyncReply{}, ErrSessionLost
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}}
	m := testManager(t, r)
	p := acquire(t, m, 1, "8.8.8.8")
	var closing atomic.Bool
	p.Bind(func() { closing.Store(true) })
	lost.Store(true)
	if err := m.Sync(t.Context()); !errors.Is(err, ErrSessionLost) || !closing.Load() {
		t.Fatal("会话失效必须停止旧连接的继续使用")
	}
	if err := m.begin(t.Context()); !errors.Is(err, ErrNotReady) {
		t.Fatal("旧连接尚未实际关闭时不能重建空会话")
	}
	p.Release()
	lost.Store(false)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	acquire(t, m, 1, "1.1.1.1")
}

func TestCloseAcknowledgmentDoesNotDiscardLaterRelease(t *testing.T) {
	var hold atomic.Bool
	captured, resume := make(chan Snapshot, 1), make(chan struct{})
	r := &testRemote{sync: func(ctx context.Context, s Snapshot) (SyncReply, error) {
		if hold.CompareAndSwap(true, false) {
			captured <- s
			select {
			case <-resume:
			case <-ctx.Done():
				return SyncReply{}, ctx.Err()
			}
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}}
	m := testManager(t, r)
	a := acquire(t, m, 1, "8.8.8.8")
	b := acquire(t, m, 1, "1.1.1.1")
	a.Release()
	hold.Store(true)
	done := make(chan error, 1)
	go func() { done <- m.Sync(t.Context()) }()
	first := <-captured
	if len(first.Retired) != 1 || first.Retired[0].Lease != a.source.lease {
		t.Fatal("关闭报告缺少已释放的来源")
	}
	b.Release()
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	remaining := m.Snapshot().Retired
	if len(remaining) != 1 || remaining[0].Lease != b.source.lease {
		t.Fatal("旧确认丢掉了稍后关闭的来源")
	}
}
