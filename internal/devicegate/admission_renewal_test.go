package devicegate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type admissionRenewalRemote struct {
	admissionSyncRemote
	leaseCache bool
}

func (*admissionRenewalRemote) DeviceRenewalSupported() bool { return true }
func (r *admissionRenewalRemote) BeginDeviceSession(_ context.Context, run string) (BeginReply, error) {
	reply := BeginReply{Version: 1, Run: run}
	if r.leaseCache {
		reply.LeaseCache = 1
	}
	return reply, nil
}

func (r *admissionRenewalRemote) receipt(s Snapshot) SyncReply {
	reply := SyncReply{Run: s.Run, Sequence: s.Sequence, ServerTime: time.Now().UnixMilli()}
	if r.leaseCache {
		reply.LeaseCache = 1
	}
	return reply
}

func admissionRenewalManager(t *testing.T, r *admissionRenewalRemote) *Manager {
	t.Helper()
	m := New(r, nil)
	m.ctx, m.cancel = context.WithCancel(t.Context())
	t.Cleanup(m.cancel)
	m.enabled.Store(true)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRenewalAfterAdmissionReestablishesBaselineWithoutRejectedRequest(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, lostReply := range []bool{false, true} {
			name := map[bool]string{false: "combined", true: "lease-cache"}[cached]
			if lostReply {
				name += "-lost-reply"
			}
			t.Run(name, func(t *testing.T) {
				r := &admissionRenewalRemote{leaseCache: cached}
				var panelBase uint64
				var sent []Snapshot
				r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
					sent = append(sent, s)
					if s.Unchanged && s.BaseSequence != panelBase {
						t.Errorf("使用旧基线续期：node=%d panel=%d", s.BaseSequence, panelBase)
						return SyncReply{}, ErrSnapshotRequired
					}
					if !s.Unchanged {
						panelBase = s.Sequence
					}
					return r.receipt(s), nil
				}
				r.combined = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
					panelBase = req.Snapshot.Sequence
					if lostReply {
						return AdmissionReply{}, errors.New("完整快照已保存，但回包丢失")
					}
					return AdmissionReply{Status: "denied", Reason: "limit", Sync: &SyncReply{
						Run: req.Run, Sequence: req.Snapshot.Sequence,
					}}, nil
				}
				m := admissionRenewalManager(t, r)
				if err := m.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); err == nil {
					t.Fatal("模拟拒绝或回包丢失不应放行")
				}
				sent = nil
				if err := m.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(sent) != 1 || sent[0].Unchanged {
					t.Fatal("准入改变基线后应直接补发完整状态，不先发送必定失败的续期")
				}
				if err := m.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				if len(sent) != 2 || !sent[1].Unchanged || sent[1].BaseSequence != sent[0].Sequence {
					t.Fatal("稳定状态未恢复轻量续期")
				}
			})
		}
	}
}

func TestInFlightAdmissionDoesNotBlockSyncOrEstablishRenewalBaseline(t *testing.T) {
	r := &admissionRenewalRemote{}
	var sent []Snapshot
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		sent = append(sent, s)
		return r.receipt(s), nil
	}
	m := admissionRenewalManager(t, r)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	r.combined = func(ctx context.Context, req AdmissionRequest) (AdmissionReply, error) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
		}
		return AdmissionReply{}, errors.New("确认丢失")
	}
	snapshot := m.Snapshot()
	go func() {
		defer close(done)
		_, _ = m.admitSnapshot(t.Context(), AdmissionRequest{Run: snapshot.Run, Snapshot: &snapshot})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := m.Sync(ctx); err != nil {
		t.Fatal("在途准入阻塞周期同步：", err)
	}
	if sent[len(sent)-1].Unchanged || m.baseSequence != 0 || m.confirmed != nil {
		t.Fatal("在途准入期间不能确认续期基线")
	}
	unblock()
	<-done
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if sent[len(sent)-1].Unchanged {
		t.Fatal("在途申请结束后仍须重新确认完整状态")
	}
}

func TestAdmissionDuringSyncCannotBeOverwrittenByLateConfirmation(t *testing.T) {
	r := &admissionRenewalRemote{}
	m := admissionRenewalManager(t, r)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	t.Cleanup(unblock)
	r.sync = func(ctx context.Context, s Snapshot) (SyncReply, error) {
		close(entered)
		select {
		case <-resume:
		case <-ctx.Done():
		}
		return r.receipt(s), nil
	}
	done := make(chan error, 1)
	go func() { done <- m.Sync(t.Context()) }()
	<-entered
	r.combined = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		return AdmissionReply{Status: "denied", Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}, nil
	}
	if _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); err == nil {
		t.Fatal("拒绝的申请不应放行")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.baseSequence != 0 || m.confirmed != nil {
		t.Fatal("迟到的同步确认覆盖了准入造成的基线变化")
	}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		if s.Unchanged {
			t.Fatal("乱序确认后必须发送完整快照")
		}
		return r.receipt(s), nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}
