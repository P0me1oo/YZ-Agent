package service

import (
	"context"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gofrs/uuid/v5"
)

type delayedRealtimeSource struct {
	controlplane.ControlPlane
	started  chan struct{}
	release  chan struct{}
	snapshot controlplane.Snapshot
}

func (s *delayedRealtimeSource) SupportsPolling() bool { return true }
func (s *delayedRealtimeSource) Poll(ctx context.Context) (controlplane.Snapshot, error) {
	close(s.started)
	select {
	case <-s.release:
		return s.snapshot, nil
	case <-ctx.Done():
		return controlplane.Snapshot{}, ctx.Err()
	}
}

func TestLateHTTPResultCannotReplaceNewWebSocketSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kernel := &fakeKernel{running: true}
	s := newTestService(kernel)
	oldConfig := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
	newConfig := &model.NodeSpec{Protocol: "vless", ServerPort: 10002}
	oldUsers := []model.UserSpec{{ID: 1, UUID: uuid.Must(uuid.NewV4()).String()}}
	newUsers := []model.UserSpec{{ID: 2, UUID: uuid.Must(uuid.NewV4()).String()}}
	s.lastConfig, s.lastConfigHash = oldConfig, computeConfigHash(oldConfig)
	s.updateUserState(oldUsers)
	s.controlVersion.accept(panel.StateVersion{Epoch: "test-control", Sequence: 1})
	s.pullResults = make(chan pullResult, 1)
	s.certRenewed = func() bool { return true }
	delayed := &delayedRealtimeSource{
		started: make(chan struct{}), release: make(chan struct{}),
		snapshot: controlplane.Snapshot{Config: oldConfig, Users: oldUsers, ControlVersion: panel.StateVersion{Epoch: "test-control", Sequence: 2}},
	}
	s.source = delayed
	s.pullViaAPIAsync(ctx)
	<-delayed.started
	s.handleWSEvent(ctx, controlplane.Event{
		Type: controlplane.EventSyncSnapshot, Version: panel.StateVersion{Epoch: "test-control", Sequence: 3},
		Config: newConfig, Users: newUsers,
	})
	close(delayed.release)
	select {
	case result := <-s.pullResults:
		s.applyPullResult(ctx, result)
	case <-time.After(time.Second):
		t.Fatal("模拟的慢 HTTP 请求没有结束")
	}
	if s.lastConfig.ServerPort != 10002 || s.lastUserHash != computeUserHash(newUsers) {
		t.Fatal("迟到的 HTTP 结果覆盖了最新推送")
	}
	if !s.pendingCertReload {
		t.Fatal("丢弃旧响应时丢失了证书续期通知")
	}
}

func TestVersionedSnapshotCanClearAllUsers(t *testing.T) {
	kernel := &fakeKernel{running: true}
	s := newTestService(kernel)
	cfg := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
	s.lastConfig, s.lastConfigHash = cfg, computeConfigHash(cfg)
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: uuid.Must(uuid.NewV4()).String()}})
	s.handleWSEvent(context.Background(), controlplane.Event{
		Type: controlplane.EventSyncSnapshot, Version: panel.StateVersion{Epoch: "test-control", Sequence: 1},
		Config: cfg, Users: []model.UserSpec{},
	})
	if len(s.lastUsers) != 0 || kernel.updateCalls != 1 {
		t.Fatal("空用户快照没有应用")
	}
}

func TestFullUserSnapshotHashIncludesConnectionLimits(t *testing.T) {
	users := []model.UserSpec{{ID: 1, UUID: uuid.Must(uuid.NewV4()).String(), ConnLimit: 10, ConnRateLimit: 20}}
	before := computeUserHash(users)
	users[0].ConnLimit++
	if before == computeUserHash(users) {
		t.Fatal("并发连接上限没有参与用户快照比较")
	}
	before = computeUserHash(users)
	users[0].ConnRateLimit++
	if before == computeUserHash(users) {
		t.Fatal("新建连接上限没有参与用户快照比较")
	}
}

func TestRelayPermissionOnlySnapshotIsAppliedAndOldSnapshotCannotRestoreAccess(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	cfg := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
	user := model.UserSpec{ID: 1, UUID: uuid.Must(uuid.NewV4()).String(), RelayRoutes: []int{11, 12}}
	s.lastConfig, s.lastConfigHash = cfg, computeConfigHash(cfg)
	s.updateUserState([]model.UserSpec{user})
	oldHash := computeUserHash([]model.UserSpec{user})
	user.RelayRoutes = []int{11}
	if oldHash == computeUserHash([]model.UserSpec{user}) {
		t.Fatal("权限变化没有改变用户摘要")
	}
	apply := func(sequence uint64, current model.UserSpec) {
		s.handleWSEvent(context.Background(), controlplane.Event{
			Type: controlplane.EventSyncSnapshot, Version: panel.StateVersion{Epoch: "relay-permission", Sequence: sequence},
			Config: cfg, Users: []model.UserSpec{current},
		})
	}
	apply(2, user)
	if k.updateCalls != 1 {
		t.Fatal("只变化线路权限时没有调用内核更新")
	}
	apply(2, user)
	user.RelayRoutes = []int{11, 12}
	apply(1, user)
	if k.updateCalls != 1 || s.lastUsers[0].AllowsRelayRoute(12) {
		t.Fatal("重复或旧快照恢复了已撤销权限")
	}
}
