package devicegate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type admissionSyncRemote struct {
	testRemote
	combined func(context.Context, AdmissionRequest) (AdmissionReply, error)
}

func (*admissionSyncRemote) DeviceAdmissionSyncSupported() bool { return true }
func (r *admissionSyncRemote) AdmitDeviceSource(ctx context.Context, req AdmissionRequest) (AdmissionReply, error) {
	n := r.calls.Add(1)
	if r.combined != nil {
		return r.combined(ctx, req)
	}
	return combinedGrant(req, n), nil
}

func combinedGrant(req AdmissionRequest, n int64) AdmissionReply {
	return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", n),
		Sync: &SyncReply{Run: req.Snapshot.Run, Sequence: req.Snapshot.Sequence}}
}

func combinedManager(t *testing.T, r *admissionSyncRemote) *Manager {
	t.Helper()
	m := New(r, nil)
	m.ctx, m.cancel = context.WithCancel(t.Context())
	m.enabled.Store(true)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func TestCombinedAdmissionIncludesLatestStateWithoutSeparateSync(t *testing.T) {
	var syncCalls atomic.Int64
	r := &admissionSyncRemote{}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		syncCalls.Add(1)
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	m := combinedManager(t, r)
	a := acquire(t, m, 1, "8.8.8.8")
	a.NewConnection()
	b := acquire(t, m, 2, "1.1.1.1")
	b.Release()
	r.combined = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		s := req.Snapshot
		if s == nil || s.Unchanged || s.Run != req.Run || s.Sequence < req.Sequence || !slices.Contains(s.Pending, req.Sequence) {
			t.Fatal("申请遗漏完整状态或自己的待处理序号")
		}
		if len(s.Sources) != 1 || s.Sources[0].ConnectSequence != a.source.sequence || len(s.Retired) != 1 {
			t.Fatal("申请未携带最新连接排序和其他账号关闭状态")
		}
		return combinedGrant(req, 3), nil
	}
	acquire(t, m, 1, "9.9.9.9")
	if syncCalls.Load() != 0 || r.calls.Load() != 3 {
		t.Fatal("首次申请仍拆成同步和授权两个请求")
	}
	m.mu.Lock()
	retired := len(m.retired)
	m.mu.Unlock()
	if retired != 1 {
		t.Fatal("其他账号尚未协调的关闭标记被提前删除")
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedAdmissionsRunInParallelAndCancelIndependently(t *testing.T) {
	const count = 32
	entered := make(chan struct{}, count+1)
	resume := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	r := &admissionSyncRemote{combined: func(ctx context.Context, req AdmissionRequest) (AdmissionReply, error) {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return AdmissionReply{}, ctx.Err()
		case <-resume:
			return combinedGrant(req, int64(req.Sequence)), nil
		}
	}}
	m := combinedManager(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	canceled := make(chan error, 1)
	go func() { p, err := m.Acquire(ctx, 1, "8.8.4.4"); p.Release(); canceled <- err }()
	results := make(chan error, count)
	for i := range count {
		go func() { p, err := m.Acquire(t.Context(), i+2, "8.8.8.8"); p.Release(); results <- err }()
	}
	for range count + 1 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("不同账号被全局同步锁串行阻塞")
		}
	}
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	unblock()
	for range count {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	m.callWG.Wait()
	if s := m.Snapshot(); len(s.Pending) != 0 || len(s.Sources) != 0 || r.calls.Load() != count+1 {
		t.Fatal("并发完成后遗留申请、来源或发生重复请求")
	}
}

func TestCombinedAdmissionWaitsForRealCloseAndRetriesWithFreshSnapshot(t *testing.T) {
	r := &admissionSyncRemote{}
	m := combinedManager(t, r)
	old := acquire(t, m, 1, "8.8.8.8")
	var closeRequested atomic.Bool
	old.Bind(func() { closeRequested.Store(true) })
	var attempts int
	r.combined = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		attempts++
		if attempts == 1 {
			return AdmissionReply{Status: "waiting", Revoked: []Revocation{{UserID: 1, IP: "8.8.8.8", Lease: old.source.lease}},
				Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
		}
		if !closeRequested.Load() || old.Valid() || (attempts == 2 && len(req.Snapshot.Sources) != 1) {
			t.Fatal("关闭完成前丢失来源记录")
		}
		old.Release() // 模拟底层第二轮才确认关闭。
		if attempts == 2 {
			return AdmissionReply{Status: "waiting", Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
		}
		if len(req.Snapshot.Sources) != 0 || len(req.Snapshot.Retired) != 1 {
			t.Fatal("重试仍使用撤销前的旧状态")
		}
		return combinedGrant(req, 2), nil
	}
	acquire(t, m, 1, "1.1.1.1")
	if attempts != 3 {
		t.Fatal("未等待真实关闭")
	}
}

func TestCombinedAdmissionRejectsMissingAckAndLateGrant(t *testing.T) {
	for _, name := range []string{"missing", "mismatch", "late"} {
		t.Run(name, func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			r := &admissionSyncRemote{combined: func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
				reply := combinedGrant(req, 1)
				switch name {
				case "missing":
					reply.Sync = nil
				case "mismatch":
					reply.Sync.Sequence++
				case "late":
					close(entered)
					<-resume
				}
				return reply, nil
			}}
			m := combinedManager(t, r)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { p, err := m.Acquire(ctx, 1, "8.8.8.8"); p.Release(); done <- err }()
			if name == "late" {
				<-entered
			}
			if err := <-done; err == nil {
				t.Fatal("无有效确认仍放行")
			}
			if name == "late" {
				close(resume)
			}
			m.callWG.Wait()
			if s := m.Snapshot(); len(s.Sources) != 0 || len(s.Pending) != 0 {
				t.Fatal("迟到授权遗留在线来源")
			}
		})
	}
}

func TestCombinedAdmissionSessionLossClosesOldPermitsAndClearsRenewalBase(t *testing.T) {
	r := &admissionSyncRemote{}
	m := combinedManager(t, r)
	old := acquire(t, m, 1, "8.8.8.8")
	old.Bind(old.Release)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.combined = func(context.Context, AdmissionRequest) (AdmissionReply, error) {
		return AdmissionReply{}, ErrSessionLost
	}
	if _, err := m.Acquire(t.Context(), 1, "1.1.1.1"); !errors.Is(err, ErrSessionLost) {
		t.Fatal(err)
	}
	if old.Valid() || m.ready {
		t.Fatal("会话丢失仍使用旧授权")
	}
	r.combined = nil
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.confirmed != nil || m.baseSequence != 0 {
		t.Fatal("新会话继承旧续期基线")
	}
	acquire(t, m, 1, "1.1.1.1")
}

func TestCombinedAdmissionPanelRollbackMustSyncBeforeLegacyRetry(t *testing.T) {
	var order []string
	var m *Manager
	r := &admissionSyncRemote{}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		order = append(order, "sync")
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	r.combined = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		if req.Snapshot != nil {
			order = append(order, "combined")
			// 并发成功响应已覆盖本次序号，回滚仍必须实际重新同步。
			m.mu.Lock()
			m.synced = req.Snapshot.Sequence
			m.mu.Unlock()
			return AdmissionReply{}, ErrAdmissionSyncUnsupported
		}
		order = append(order, "legacy")
		return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", 2)}, nil
	}
	m = combinedManager(t, r)
	acquire(t, m, 1, "8.8.8.8")
	if !slices.Equal(order, []string{"combined", "sync", "legacy"}) {
		t.Fatal(order)
	}
}

func TestCombinedAdmissionSameSourceCancellationPreservesOtherWaiters(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	r := &admissionSyncRemote{combined: func(ctx context.Context, req AdmissionRequest) (AdmissionReply, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return AdmissionReply{}, ctx.Err()
		case <-resume:
			return combinedGrant(req, 1), nil
		}
	}}
	m := combinedManager(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled := make(chan error, 1)
	go func() { p, err := m.Acquire(ctx, 1, "8.8.8.8"); p.Release(); canceled <- err }()
	<-entered
	live := make(chan error, 32)
	for range 32 {
		go func() { p, err := m.Acquire(t.Context(), 1, "8.8.8.8"); p.Release(); live <- err }()
	}
	waitForAdmissions(t, m, 33)
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	unblock()
	for range 32 {
		if err := <-live; err != nil {
			t.Fatal(err)
		}
	}
	m.callWG.Wait()
	if r.calls.Load() != 1 {
		t.Fatal("同来源取消导致重新申请")
	}
}

func TestCombinedAdmissionDoesNotQueueBehindBusyPeriodicSync(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	r := &admissionSyncRemote{}
	r.sync = func(ctx context.Context, s Snapshot) (SyncReply, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return SyncReply{}, ctx.Err()
		case <-resume:
			return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
		}
	}
	m := combinedManager(t, r)
	done := make(chan error, 1)
	go func() { done <- m.Sync(t.Context()) }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	p, err := m.Acquire(ctx, 1, "8.8.8.8")
	close(resume)
	if syncErr := <-done; syncErr != nil {
		t.Fatal(syncErr)
	}
	r.sync = nil
	if err != nil {
		t.Fatalf("合并申请仍等待周期同步：%v", err)
	}
	p.Release()
}
