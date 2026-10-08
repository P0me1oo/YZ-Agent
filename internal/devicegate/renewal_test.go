package devicegate

import (
	"context"
	"errors"
	"testing"
)

type renewalRemote struct{ testRemote }

func (*renewalRemote) DeviceRenewalSupported() bool { return true }

func newRenewalManager(t *testing.T, remote *renewalRemote) *Manager {
	t.Helper()
	m := New(remote, nil)
	m.ctx, m.cancel = context.WithCancel(t.Context())
	m.enabled.Store(true)
	t.Cleanup(m.cancel)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRenewalOnlyFollowsConfirmedContentAndNewConnectionsSendFullSnapshots(t *testing.T) {
	var sent []Snapshot
	r := &renewalRemote{}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		sent = append(sent, s)
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	m := newRenewalManager(t, r)
	p := acquire(t, m, 1, "8.8.8.8")
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	full := sent[len(sent)-1]
	if full.Unchanged || len(full.Sources) != 1 {
		t.Fatal("新来源必须发送完整快照")
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	renewal := sent[len(sent)-1]
	if !renewal.Unchanged || renewal.BaseSequence != full.Sequence || renewal.Sources != nil || renewal.Pending != nil {
		t.Fatal("未变化来源应使用已确认基线的轻量续期")
	}
	if !p.NewConnection() {
		t.Fatal("新连接登记失败")
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	updated := sent[len(sent)-1]
	if updated.Unchanged || updated.Sources[0].ConnectSequence <= full.Sources[0].ConnectSequence {
		t.Fatal("新连接必须完整上报排序依据")
	}
	p.Release()
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	closed := sent[len(sent)-1]
	if closed.Unchanged || len(closed.Sources) != 0 || len(closed.Retired) != 1 {
		t.Fatal("实际关闭必须使用完整快照确认")
	}
}

func TestRenewalBaselineRecoveryDoesNotRevokeLiveConnections(t *testing.T) {
	r := &renewalRemote{}
	m := newRenewalManager(t, r)
	p := acquire(t, m, 1, "8.8.8.8")
	p.Bind(func() { t.Error("缺失快照基线不能关闭实际连接") })
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var sent []Snapshot
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		sent = append(sent, s)
		if s.Unchanged {
			return SyncReply{}, ErrSnapshotRequired
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || !sent[0].Unchanged || sent[1].Unchanged || sent[0].Sequence != sent[1].Sequence || len(sent[1].Sources) != 1 || !p.Valid() {
		t.Fatal("续期失败应按同一序号补发完整快照并保留连接")
	}
}

func TestUnconfirmedFullSnapshotIsNotUsedAsRenewalBaseline(t *testing.T) {
	r := &renewalRemote{}
	m := newRenewalManager(t, r)
	acquire(t, m, 1, "8.8.8.8")
	var sent []Snapshot
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		sent = append(sent, s)
		if len(sent) == 1 {
			return SyncReply{}, errors.New("模拟确认丢失")
		}
		if len(sent) == 2 {
			return SyncReply{Run: s.Run, Sequence: s.Sequence - 1}, nil
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	if m.Sync(t.Context()) == nil || m.Sync(t.Context()) == nil {
		t.Fatal("确认丢失或序号不符不得当作成功")
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, s := range sent {
		if s.Unchanged {
			t.Fatal("未确认的新内容不能用于续期")
		}
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !sent[len(sent)-1].Unchanged {
		t.Fatal("确认后的内容应可续期")
	}
}

func TestRenewalCarriesRevocationUntilRealCloseAndSessionLossRestartsFull(t *testing.T) {
	r := &renewalRemote{}
	m := newRenewalManager(t, r)
	p := acquire(t, m, 1, "8.8.8.8")
	closed := 0
	p.Bind(func() { closed++ })
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		return SyncReply{Run: s.Run, Sequence: s.Sequence, Revoked: []Revocation{{UserID: 1, IP: "8.8.8.8", Lease: p.source.lease}}}, nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || len(m.Snapshot().Sources) != 1 {
		t.Fatal("撤销只请求关闭，实际释放前必须保留来源")
	}
	r.sync = func(context.Context, Snapshot) (SyncReply, error) { return SyncReply{}, ErrSessionLost }
	if !errors.Is(m.Sync(t.Context()), ErrSessionLost) || m.ready {
		t.Fatal("丢失会话必须暂停新接入")
	}
	p.Release()
	r.sync = nil
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		if s.Unchanged {
			t.Error("新会话不能继续使用旧续期基线")
		}
		return SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}
