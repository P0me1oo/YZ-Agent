package devicegate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitForAdmissions(t *testing.T, m *Manager, count int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		m.mu.Lock()
		waiting := 0
		for _, f := range m.flights {
			waiting += len(f.waiters)
		}
		m.mu.Unlock()
		if waiting == count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("等待准入请求登记超时：got %d, want %d", waiting, count)
		case <-tick.C:
		}
	}
}

func TestAdmissionCancellationOnlyAffectsItsOwnConnection(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel_first_%v", cancelFirst), func(t *testing.T) {
			entered, resume := make(chan struct{}), make(chan struct{})
			var release sync.Once
			unblock := func() { release.Do(func() { close(resume) }) }
			defer unblock()
			remote := &testRemote{admit: func(ctx context.Context, _ AdmissionRequest) (AdmissionReply, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("共享申请必须有截止时间")
				}
				close(entered)
				select {
				case <-resume:
					return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", 1)}, nil
				case <-ctx.Done():
					return AdmissionReply{}, ctx.Err()
				}
			}}
			m := testManager(t, remote)
			canceledCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			canceled := make(chan error, 1)
			live := make(chan error, 32)
			start := func(ctx context.Context, result chan<- error) {
				go func() {
					p, err := m.Acquire(ctx, 1, "8.8.8.8")
					p.Release()
					result <- err
				}()
			}
			if cancelFirst {
				start(canceledCtx, canceled)
			} else {
				start(t.Context(), live)
			}
			<-entered
			if !cancelFirst {
				start(canceledCtx, canceled)
			}
			for range 31 {
				start(t.Context(), live)
			}
			if cancelFirst {
				start(t.Context(), live)
			}
			waitForAdmissions(t, m, 33)
			cancel()
			select {
			case err := <-canceled:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("单个取消返回错误：%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("取消连接仍等待共享申请")
			}
			unblock()
			for range 32 {
				if err := <-live; err != nil {
					t.Fatalf("有效连接被连带拒绝：%v", err)
				}
			}
			if remote.calls.Load() != 1 {
				t.Fatalf("同一批有效连接重复申请：%d", remote.calls.Load())
			}
			if snapshot := m.Snapshot(); len(snapshot.Sources) != 0 || len(snapshot.Pending) != 0 {
				t.Fatal("所有连接释放后遗留来源或申请")
			}
		})
	}
}

func TestAdmissionDeadlineDoesNotWaitForBusySync(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var stalled atomic.Bool
	var enteredOnce, resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(resume) }) }
	defer unblock()
	remote := &testRemote{sync: func(ctx context.Context, s Snapshot) (SyncReply, error) {
		if stalled.Load() {
			enteredOnce.Do(func() { close(entered) })
			select {
			case <-resume:
			case <-ctx.Done():
				return SyncReply{}, ctx.Err()
			}
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}}
	m := testManager(t, remote)
	existing := acquire(t, m, 1, "8.8.8.8")
	var revoked atomic.Bool
	existing.Bind(func() { revoked.Store(true) })
	stalled.Store(true)
	syncDone := make(chan error, 1)
	go func() { syncDone <- m.Sync(t.Context()) }()
	<-entered
	for _, operation := range []string{"sync", "admit"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if operation == "sync" {
					done <- m.Sync(ctx)
				} else {
					p, err := m.Acquire(ctx, 1, "1.1.1.1")
					p.Release()
					done <- err
				}
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("截止时间错误未保留：%v", err)
				}
			case <-time.After(500 * time.Millisecond):
				unblock()
				t.Fatal("超过截止时间仍等待同步")
			}
		})
	}
	m.callWG.Wait()
	reuse := acquire(t, m, 1, "8.8.8.8")
	if !existing.Valid() || !reuse.Valid() || revoked.Load() || remote.calls.Load() != 1 {
		t.Fatal("同步阻塞影响已有来源，或超时申请仍发送到面板")
	}
	stalled.Store(false)
	unblock()
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	acquire(t, m, 1, "1.1.1.1")
}

func TestAllCanceledAdmissionsDiscardLateGrantAndRetryAfterCleanup(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(resume) }) }
	defer unblock()
	remote := &testRemote{admit: func(_ context.Context, _ AdmissionRequest) (AdmissionReply, error) {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-resume // 模拟远端已经接收申请，取消后仍迟到返回授权。
		}
		return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", n)}, nil
	}}
	m := testManager(t, remote)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { p, err := m.Acquire(ctx, 1, "8.8.8.8"); p.Release(); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if snapshot := m.Snapshot(); len(snapshot.Sources) != 0 || len(snapshot.Pending) != 1 {
		t.Fatal("未结束申请必须继续保留序号且不能登记在线")
	}
	retried := make(chan *Permit, 1)
	retryErr := make(chan error, 1)
	go func() {
		p, err := m.Acquire(t.Context(), 1, "8.8.8.8")
		retried <- p
		retryErr <- err
	}()
	waitForAdmissions(t, m, 1)
	unblock()
	if err := <-retryErr; err != nil {
		t.Fatalf("恢复连接继承旧取消：%v", err)
	}
	p := <-retried
	defer p.Release()
	if p == nil || p.source.lease != fmt.Sprintf("%032x", 2) || calls.Load() != 2 {
		t.Fatal("迟到授权被复用，或没有重新申请")
	}
	p.Release()
	m.callWG.Wait()
	if snapshot := m.Snapshot(); len(snapshot.Pending) != 0 || len(snapshot.Sources) != 0 {
		t.Fatal("取消后恢复遗留来源或申请")
	}
}

func TestStopCancelsSharedAdmissionAndFreshManagerRecovers(t *testing.T) {
	entered := make(chan struct{})
	var stalled atomic.Bool
	stalled.Store(true)
	remote := &testRemote{admit: func(ctx context.Context, _ AdmissionRequest) (AdmissionReply, error) {
		if stalled.Load() {
			close(entered)
			<-ctx.Done()
			return AdmissionReply{}, ctx.Err()
		}
		return AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", 1)}, nil
	}}
	m := testManager(t, remote)
	done := make(chan error, 1)
	go func() { p, err := m.Acquire(t.Context(), 1, "8.8.8.8"); p.Release(); done <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrNotReady) {
		t.Fatalf("停止后申请仍成功：%v", err)
	}
	if snapshot := m.Snapshot(); len(snapshot.Pending) != 0 || len(snapshot.Sources) != 0 {
		t.Fatal("停止后遗留来源或申请")
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("重复停止失败：%v", err)
	}
	stalled.Store(false)
	fresh := testManager(t, remote)
	acquire(t, fresh, 1, "8.8.8.8")
}

func TestCanceledContextCannotReuseExistingAuthorization(t *testing.T) {
	m := testManager(t, &testRemote{})
	acquire(t, m, 1, "8.8.8.8")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p, err := m.Acquire(ctx, 1, "8.8.8.8")
	p.Release()
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("已经取消的连接仍登记授权")
	}
}

func TestSharedAdmissionDenialIsReturnedWithoutRetry(t *testing.T) {
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	remote := &testRemote{admit: func(ctx context.Context, _ AdmissionRequest) (AdmissionReply, error) {
		close(entered)
		select {
		case <-resume:
			return AdmissionReply{Status: "denied", Limit: 1, Observed: 1, Reason: "cooldown"}, nil
		case <-ctx.Done():
			return AdmissionReply{}, ctx.Err()
		}
	}}
	m := testManager(t, remote)
	done := make(chan error, 2)
	start := func() {
		go func() { p, err := m.Acquire(t.Context(), 1, "8.8.8.8"); p.Release(); done <- err }()
	}
	start()
	<-entered
	start()
	waitForAdmissions(t, m, 2)
	unblock()
	for range 2 {
		var denied *DeniedError
		if err := <-done; !errors.As(err, &denied) || denied.Reason != "cooldown" {
			t.Fatalf("设备业务拒绝丢失：%v", err)
		}
	}
	if remote.calls.Load() != 1 || len(m.Snapshot().Sources) != 0 {
		t.Fatal("业务拒绝后重试或创建了来源")
	}
}
