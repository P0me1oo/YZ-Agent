package devicegate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func recoveryAdmission(lease Lease, req AdmissionRequest) AdmissionReply {
	return AdmissionReply{Status: "allowed", Lease: lease.Lease, ConfirmedAt: lease.ConfirmedAt, ConfirmedSequence: req.Snapshot.Sequence,
		ExpiresAt: lease.ExpiresAt, ServerTime: lease.ConfirmedAt,
		Sync: &SyncReply{Run: req.Run, Sequence: req.Snapshot.Sequence}}
}

func recoverySync(lease Lease) func(context.Context, Snapshot) (SyncReply, error) {
	return func(_ context.Context, s Snapshot) (SyncReply, error) {
		lease.ConfirmedSequence = s.Sequence
		return SyncReply{Run: s.Run, Sequence: s.Sequence, LeaseCache: 1, ServerTime: lease.ConfirmedAt, Leases: []Lease{lease}}, nil
	}
}

func TestLeaseRecoveryAfterRevocationKeepsSharedIdentity(t *testing.T) {
	for _, reason := range []string{"explicit", "omitted", "users"} {
		for _, recovery := range []string{"sync", "admit"} {
			t.Run(reason+"/"+recovery, func(t *testing.T) {
				lease := testLease(1, "8.8.8.8", time.Hour)
				remote := &leaseRemote{leases: []Lease{lease}}
				m := cachedManager(t, remote)
				other := cachedManager(t, &leaseRemote{leases: []Lease{lease}})
				for _, node := range []*Manager{m, other} {
					if err := node.Sync(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				p, shared := acquire(t, m, 1, lease.IP), acquire(t, other, 1, lease.IP)
				defer shared.Release()
				var closes atomic.Int32
				p.Bind(func() { closes.Add(1); p.Release() })
				var revokedReply SyncReply
				remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
					revokedReply = SyncReply{Run: s.Run, Sequence: s.Sequence, LeaseCache: 1, ServerTime: lease.ConfirmedAt}
					if reason == "explicit" {
						revokedReply.Revoked = []Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}}
					}
					return revokedReply, nil
				}
				if reason == "users" {
					m.SetUsers([]int{2})
				} else if err := m.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				if p.Valid() || closes.Load() != 1 {
					t.Fatal("撤销必须关闭原连接且只关闭一次")
				}
				if !shared.Valid() {
					t.Fatal("单节点撤销影响了其他持有节点")
				}
				m.SetUsers([]int{1, 2})
				// 同一毫秒也可重新核验；不能只靠墙上时钟区分新旧授权。
				remote.sync = recoverySync(lease)
				remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
					return recoveryAdmission(lease, req), nil
				}
				if recovery == "sync" {
					if err := m.Sync(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				restored := acquire(t, m, 1, lease.IP)
				defer restored.Release()
				deadline := restored.source.deadline
				if restored.source.lease != shared.source.lease {
					t.Fatal("恢复改变了共享许可编号")
				}
				if reason != "users" {
					for range 2 {
						if err := m.acceptLeases(revokedReply, revokedReply.Sequence, time.Now()); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !restored.Valid() || !shared.Valid() || !restored.source.deadline.Equal(deadline) || closes.Load() != 1 {
					t.Fatal("迟到重复撤销影响恢复后的许可或期限")
				}
			})
		}
	}
}

func TestLeaseRecoveryRejectsAdmissionStartedBeforeRevocation(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{}
	m := cachedManager(t, remote)
	entered, resume := make(chan struct{}), make(chan struct{})
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		close(entered)
		<-resume
		return recoveryAdmission(lease, req), nil
	}
	done := make(chan error, 1)
	go func() { p, err := m.Acquire(t.Context(), 1, lease.IP); p.Release(); done <- err }()
	<-entered
	m.ApplyRevocations([]Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}})
	close(resume)
	if err := <-done; !errors.Is(err, ErrRevoked) {
		t.Fatalf("撤销前的在途申请被接受：%v", err)
	}
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		return recoveryAdmission(lease, req), nil
	}
	p := acquire(t, m, 1, lease.IP)
	p.Release()
}

func TestLeaseRecoveryUserRemovalFencesUnknownInFlightLicense(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{}
	m := cachedManager(t, remote)
	entered, resume := make(chan struct{}), make(chan struct{})
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		close(entered)
		<-resume
		return recoveryAdmission(lease, req), nil
	}
	done := make(chan error, 1)
	go func() { p, err := m.Acquire(t.Context(), 1, lease.IP); p.Release(); done <- err }()
	<-entered
	m.SetUsers([]int{2})
	m.SetUsers([]int{1, 2})
	close(resume)
	if err := <-done; !errors.Is(err, ErrRevoked) {
		t.Fatalf("用户恢复放行了移除前的未知许可：%v", err)
	}
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		return recoveryAdmission(lease, req), nil
	}
	p := acquire(t, m, 1, lease.IP)
	p.Release()
}

func TestLeaseRecoveryRequiresCurrentPanelConfirmation(t *testing.T) {
	for _, mode := range []string{"missing", "old", "conflicting"} {
		t.Run(mode, func(t *testing.T) {
			lease := testLease(1, "8.8.8.8", time.Hour)
			remote := &leaseRemote{leases: []Lease{lease}}
			m := cachedManager(t, remote)
			if err := m.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			p := acquire(t, m, 1, lease.IP)
			p.Bind(p.Release)
			revocation := Revocation{UserID: 1, IP: lease.IP, Lease: lease.Lease}
			m.ApplyRevocations([]Revocation{revocation})
			remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
				lease.ConfirmedSequence = 0
				if mode == "old" {
					lease.ConfirmedSequence = s.Sequence - 1
				}
				reply := SyncReply{Run: s.Run, Sequence: s.Sequence, LeaseCache: 1, ServerTime: lease.ConfirmedAt, Leases: []Lease{lease}}
				if mode == "conflicting" {
					reply.Leases[0].ConfirmedSequence = s.Sequence
					reply.Revoked = []Revocation{revocation}
				}
				return reply, nil
			}
			if err := m.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			if next, err := m.Acquire(t.Context(), 1, lease.IP); err == nil {
				next.Release()
				t.Fatal("未核验或矛盾许可解除了撤销")
			}
			remote.sync = recoverySync(lease)
			if err := m.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			restored := acquire(t, m, 1, lease.IP)
			restored.Release()
		})
	}
}

func TestLeaseRecoveryRejectsPreviousSessionReply(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, lease.IP)
	p.Bind(p.Release)
	old := m.Snapshot()
	m.loseSession(old.Run)
	if p.Valid() {
		t.Fatal("旧会话连接未关闭")
	}
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	remote.sync = recoverySync(lease)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored := acquire(t, m, 1, lease.IP)
	defer restored.Release()
	reply := SyncReply{Run: old.Run, Sequence: old.Sequence, LeaseCache: 1, ServerTime: lease.ConfirmedAt,
		Revoked: []Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}}}
	if err := m.acceptLeases(reply, old.Sequence, time.Now()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("旧会话响应未拒绝：%v", err)
	}
	if !restored.Valid() {
		t.Fatal("旧会话撤销影响了重新握手的许可")
	}
}

func TestLeaseRecoveryRejectsInFlightSyncEvenWithNewerConfirmationTime(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, lease.IP)
	p.Bind(p.Release)
	old := m.Snapshot()
	m.ApplyRevocations([]Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}})
	lease.ConfirmedAt++
	lease.ExpiresAt++
	late := SyncReply{Run: old.Run, Sequence: old.Sequence, LeaseCache: 1, ServerTime: lease.ConfirmedAt, Leases: []Lease{lease}}
	for range 2 {
		if err := m.acceptLeases(late, old.Sequence, time.Now()); err != nil {
			t.Fatal(err)
		}
		if p, err := m.Acquire(t.Context(), 1, lease.IP); err == nil {
			p.Release()
			t.Fatal("在途旧同步恢复了撤销许可")
		}
	}
	remote.leases = []Lease{lease}
	remote.sync = recoverySync(lease)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored := acquire(t, m, 1, lease.IP)
	defer restored.Release()
	deadline := restored.source.deadline
	if err := m.acceptLeases(late, old.Sequence, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !restored.Valid() || !restored.source.deadline.Equal(deadline) {
		t.Fatal("旧授权覆盖了恢复后的许可")
	}
}

func TestLeaseRecoveryWaitsForRealCloseAndRejectsFailedVerification(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, lease.IP)
	m.ApplyRevocations([]Revocation{{UserID: 1, IP: lease.IP, Lease: lease.Lease}})
	remote.sync = recoverySync(lease)
	remote.admit = func(_ context.Context, req AdmissionRequest) (AdmissionReply, error) {
		return recoveryAdmission(lease, req), nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if next, err := m.Acquire(t.Context(), 1, lease.IP); err == nil {
		next.Release()
		t.Fatal("真实关闭前复用了已撤销来源")
	}
	p.Release()
	remote.admit = nil
	remote.sync = func(context.Context, Snapshot) (SyncReply, error) { return SyncReply{}, errors.New("测试断联") }
	if err := m.Sync(t.Context()); err == nil {
		t.Fatal("未模拟核验失败")
	}
	if next, err := m.Acquire(t.Context(), 1, lease.IP); err == nil {
		next.Release()
		t.Fatal("核验失败后自行恢复")
	}
	remote.sync = recoverySync(lease)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored := acquire(t, m, 1, lease.IP)
	restored.Release()
}
