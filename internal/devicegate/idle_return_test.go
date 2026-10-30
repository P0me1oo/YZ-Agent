package devicegate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// 通过线上的 JSON 格式构造扩展响应，也能在修复前复现旧客户端忽略归还指令。
func idleReply(t *testing.T, run string, seq uint64, lease Lease) SyncReply {
	t.Helper()
	data, err := json.Marshal(map[string]any{"run": run, "sequence": seq, "lease_cache": 1,
		"server_time": time.Now().UnixMilli(), "leases": []Lease{lease}, "idle": []Revocation{{UserID: lease.UserID, IP: lease.IP, Lease: lease.Lease}}})
	if err != nil {
		t.Fatal(err)
	}
	var reply SyncReply
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestOnlineIdleReturnAfterLastConnectionAndNoLateResurrection(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	lease.ConfirmedSequence = 1
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, b := acquire(t, m, 1, lease.IP), acquire(t, m, 1, lease.IP)
	a.Release()
	remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		return idleReply(t, s.Run, s.Sequence, lease), nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !b.Valid() || len(m.Snapshot().Retired) != 0 {
		t.Fatal("共享来源还有连接时不能归还")
	}
	b.Release()
	// 同步失败前仍保留固定期限内的离线重连能力。
	remote.sync = func(context.Context, Snapshot) (SyncReply, error) { return SyncReply{}, errors.New("断联") }
	if err := m.Sync(t.Context()); err == nil {
		t.Fatal("预期断联")
	}
	c := acquire(t, m, 1, lease.IP)
	c.Release()
	remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		return idleReply(t, s.Run, s.Sequence, lease), nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot()
	if len(s.Retired) != 1 || len(m.sources) != 0 {
		t.Fatal("在线空闲许可没有归还")
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(m.sources) != 0 {
		t.Fatal("重复旧许可回包复活已归还许可")
	}
	if _, err := m.Acquire(t.Context(), 1, lease.IP); err == nil {
		t.Fatal("归还后必须重新申请")
	}
}

func TestIdleResponseRacingNewConnectionDoesNotCloseIt(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	started, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		close(started)
		<-resume
		return idleReply(t, s.Run, s.Sequence, lease), nil
	}
	go func() { done <- m.Sync(t.Context()) }()
	<-started
	p := acquire(t, m, 1, lease.IP)
	defer p.Release()
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !p.Valid() || len(m.Snapshot().Retired) != 0 {
		t.Fatal("空闲快照之后的新连接被归还关闭")
	}
}

func TestSessionLossReturnCarriesOriginalRunAndConfirmation(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	lease.ConfirmedSequence = 1
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := acquire(t, m, 1, lease.IP)
	p.Bind(p.Release)
	oldRun := m.run
	m.loseSession(oldRun)
	if err := m.begin(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot()
	data, err := json.Marshal(s.Retired)
	if err != nil {
		t.Fatal(err)
	}
	var retired []map[string]any
	if err := json.Unmarshal(data, &retired); err != nil {
		t.Fatal(err)
	}
	if s.Run == oldRun || len(retired) != 1 || retired[0]["run"] != oldRun || retired[0]["confirmed_sequence"] != float64(1) {
		t.Fatalf("旧会话归还丢失原会话或确认身份: %s", data)
	}
}

func TestIdleReturnAcknowledgesConfirmationWhoseEarlierResponseWasLost(t *testing.T) {
	lease := testLease(1, "8.8.8.8", time.Hour)
	lease.ConfirmedSequence = 1
	remote := &leaseRemote{leases: []Lease{lease}}
	m := cachedManager(t, remote)
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	remote.sync = func(_ context.Context, s Snapshot) (SyncReply, error) {
		return idleReply(t, s.Run, s.Sequence, lease), nil
	}
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	// 另一次在途核验已写入面板，但回包未安装。下一次归还需确认它的身份，
	// 不能重建本地许可，也不能一直携带过时的确认编号。
	lease.ConfirmedSequence = m.Snapshot().Sequence
	if err := m.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	s := m.Snapshot()
	data, _ := json.Marshal(s.Retired)
	var retired []map[string]any
	if err := json.Unmarshal(data, &retired); err != nil {
		t.Fatal(err)
	}
	if len(m.sources) != 0 || len(retired) != 1 || retired[0]["confirmed_sequence"] != float64(lease.ConfirmedSequence) {
		t.Fatalf("空闲归还没有跟进已知的核验身份: %s", data)
	}
}
