package service

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

// 全量、增量和轮询都不能修改内部入站；即使旧面板误推了用户也应保持运行。
func TestLandingIgnoresPanelUserEvents(t *testing.T) {
	ctx := context.Background()
	for _, running := range []bool{true, false} {
		k := &fakeKernel{running: running, protocols: []string{"shadowsocks"},
			updateErr: errors.New("不支持更新"), addErr: errors.New("不支持新增"), removeErr: errors.New("不支持删除")}
		s := newTestService(k)
		s.lastConfig = landingConfig()
		users := []model.UserSpec{{ID: 9, UUID: "test-only-user"}}
		s.lastUsers = users // 模拟旧版本留下的错误状态。
		for range 2 {
			for _, event := range []controlplane.Event{
				{Type: controlplane.EventSyncUsers, Users: users},
				{Type: controlplane.EventSyncUserDelta, DeltaAction: "add", DeltaUsers: users},
				{Type: controlplane.EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: users},
				{Type: controlplane.EventSyncUsers, Users: []model.UserSpec{}},
			} {
				s.handleWSEvent(ctx, event)
				if !k.running || s.runtimeError != nil || len(s.lastUsers) != 0 || len(s.appliedState.Users) != 0 {
					t.Fatal("用户事件停止了落地或污染了用户状态")
				}
			}
			s.applyPullResult(ctx, pullResult{users: users, userHash: computeUserHash(users)})
			s.applyPullResult(ctx, pullResult{config: landingConfig(), users: users, userHash: computeUserHash(users)})
			if !k.running || len(s.lastUsers) != 0 || s.lastUserHash != computeUserHash(nil) {
				t.Fatal("轮询或配置重载污染了落地用户状态")
			}
		}
		if k.addCalls+k.removeCalls+k.updateCalls != 0 || len(k.startUsers) != 0 {
			t.Fatal("落地调用了普通用户更新接口")
		}
	}
}

func TestLandingUserEventDoesNotBypassFailedConfig(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}, startErr: errors.New("启动失败")}
	s := newTestService(k)
	s.lastConfig = landingConfig()
	for range 2 {
		if s.applyUserUpdate(context.Background(), []model.UserSpec{{ID: 9}}, "ignored") {
			t.Fatal("配置失败被用户事件掩盖")
		}
	}
	if k.running || k.startCalls != 1 || s.runtimeError == nil {
		t.Fatal("失败配置被重复启动或未保留错误")
	}
}

func TestLandingCanReturnToPlainNodeWithPolledUsers(t *testing.T) {
	k := &fakeKernel{running: true, protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	s.lastConfig = landingConfig()
	nc := *landingConfig()
	nc.Relay = nil
	nc.Cipher = "aes-128-gcm"
	users := []model.UserSpec{{ID: 9, UUID: "test-only-user"}}
	s.applyPullResult(context.Background(), pullResult{config: &nc, users: users, userHash: computeUserHash(users)})
	if len(s.lastUsers) != 1 || !k.running || k.reloadCalls != 1 {
		t.Fatal("切回普通节点时丢失同批用户")
	}
}

func landingConfig() *model.NodeSpec {
	return &model.NodeSpec{
		Protocol:   "shadowsocks",
		ServerPort: 28388,
		Relay: &model.RelayConfig{
			Mode:       panel.RelayModeLanding,
			Protocol:   "shadowsocks",
			ListenPort: 28388,
			Cipher:     "2022-blake3-aes-128-gcm",
			Password:   "MTIzNDU2Nzg5MGFiY2RlZg==",
		},
	}
}

func entryConfig() *model.NodeSpec {
	return &model.NodeSpec{
		Protocol:   "vless",
		ServerPort: 24443,
		Relay: &model.RelayConfig{
			Mode:    panel.RelayModeEntry,
			RouteID: 11,
			Children: []model.RelayChild{{
				NodeID: 7, Tag: "relay-7", RouteID: 12,
				Protocol: "shadowsocks", Address: "203.0.113.7", Port: 28388,
				Cipher: "2022-blake3-aes-128-gcm", Password: "MTIzNDU2Nzg5MGFiY2RlZg==",
			}},
		},
	}
}

// The landing node has no panel users by design, so the kernel must still come up.
func TestEnsureRunning_LandingStartsWithoutUsers(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	s.lastConfig = landingConfig()

	if !s.ensureRunning(context.Background()) {
		t.Fatal("landing kernel did not start with an empty user set")
	}
	if k.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", k.startCalls)
	}
}

func TestEnsureRunning_PlainNodeStillNeedsUsers(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 443}

	if s.ensureRunning(context.Background()) {
		t.Fatal("plain node started without users")
	}
	if k.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0", k.startCalls)
	}
}

// A config re-sync must not shut a landing node down just because it reports no users.
func TestApplyChanges_LandingSurvivesEmptyUserSet(t *testing.T) {
	k := &fakeKernel{running: true, protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	s.lastConfig = landingConfig()
	s.updateUserState(nil)

	s.applyChanges(context.Background(), true, false)

	if !k.running {
		t.Fatal("landing kernel was stopped on an empty user set")
	}
}

func TestApplyChanges_PlainNodeStopsOnEmptyUserSet(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 443}
	s.updateUserState(nil)

	s.applyChanges(context.Background(), true, false)

	if k.running {
		t.Fatal("plain node kept running with no users")
	}
}

// Relay traffic collection is entry-only and must tolerate kernels without the
// optional capability.
func TestTrackRelayTraffic_SkipsNonEntryAndPlainKernel(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.tracker = tracker.New()

	s.lastConfig = landingConfig()
	s.trackRelayTraffic(context.Background())

	s.lastConfig = entryConfig()
	s.trackRelayTraffic(context.Background()) // fakeKernel is not a RelayTrafficReader

	if got := s.tracker.FlushRelayTraffic(); got != nil {
		t.Fatalf("relay traffic = %v, want nil", got)
	}
}
