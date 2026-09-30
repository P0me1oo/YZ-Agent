package controlplane

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestNodeMailboxPreservesEmptyConfigurationLists(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "未提供列表"
		cfg := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
		if empty {
			name = "明确的空列表"
			cfg.Routes = []model.RouteRule{}
			cfg.CustomOutbounds = []model.OutboundConfig{}
			cfg.CustomRouteRules = []model.CustomRouteRule{}
		}
		t.Run(name, func(t *testing.T) {
			before, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			mb := NewNodeMailbox()
			mb.MarkReady()
			mb.Apply(Event{Type: EventSyncConfig, Config: cfg})
			after, err := json.Marshal(mb.DrainIfReady().Config)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("经过机器共享连接后，未变化配置的序列化结果改变了")
			}
		})
	}
}

func TestNodeMailboxLatestStateAndNotifyCollapse(t *testing.T) {
	mb := NewNodeMailbox()
	cfg1 := &model.NodeSpec{Protocol: "vmess", ServerPort: 10010}
	cfg2 := &model.NodeSpec{Protocol: "vless", ServerPort: 10020}

	mb.Apply(Event{Type: EventSyncConfig, Config: cfg1})
	mb.Apply(Event{Type: EventSyncConfig, Config: cfg2})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected collapsed notify signal")
	}

	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.HasConfig || state.Config == nil {
		t.Fatal("expected config state")
	}
	if state.Config.Protocol != "vless" || state.Config.ServerPort != 10020 {
		t.Fatalf("unexpected config: %+v", state.Config)
	}
}

func TestNodeMailboxFullUsersOverwriteAndDeltaBeforeBaseline(t *testing.T) {
	mb := NewNodeMailbox()
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 1, UUID: "u1"}},
	})
	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected reconcile when delta arrives before baseline")
	}

	mb.Apply(Event{Type: EventSyncUsers, Users: []model.UserSpec{{ID: 1, UUID: "u1"}}})
	mb.Apply(Event{Type: EventSyncUsers, Users: []model.UserSpec{{ID: 2, UUID: "u2"}}})
	state = mb.DrainIfReady()
	if !state.HasUsers {
		t.Fatal("expected users snapshot")
	}
	if len(state.Users) != 1 || state.Users[0].ID != 2 {
		t.Fatalf("unexpected users: %+v", state.Users)
	}
}

func TestNodeMailboxDevicesLatestWins(t *testing.T) {
	mb := NewNodeMailbox()
	mb.Apply(Event{Type: EventSyncDevices, DeviceUsers: map[int][]string{1: {"1.1.1.1"}}})
	mb.Apply(Event{Type: EventSyncDevices, DeviceUsers: map[int][]string{1: {"2.2.2.2"}}})
	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.HasDevices {
		t.Fatal("expected devices state")
	}
	if got := state.DeviceUsers[1][0]; got != "2.2.2.2" {
		t.Fatalf("unexpected device state: %v", state.DeviceUsers)
	}
}

func TestNodeMailboxReconcileNotifies(t *testing.T) {
	mb := NewNodeMailbox()
	mb.MarkReady()

	// Drain the MarkReady notification
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Delta without baseline should trigger reconcile AND notify
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 1, UUID: "u1"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify on reconcile path")
	}

	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected NeedsReconcile")
	}
}

func TestNodeMailboxSeedBaselineEnablesDelta(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}, nil)
	mb.MarkReady()

	// Drain MarkReady notify
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Delta should now apply incrementally, not trigger reconcile
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 3, UUID: "u3"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify after delta")
	}

	state := mb.DrainIfReady()
	if state.NeedsReconcile {
		t.Fatal("should not need reconcile after seeded baseline")
	}
	if !state.HasUsers {
		t.Fatal("expected users")
	}
	if len(state.Users) != 3 {
		t.Fatalf("expected 3 users, got %d: %+v", len(state.Users), state.Users)
	}
}

func TestNodeMailboxDeltaFailedApplyNotifies(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "u1"}}, nil)
	mb.MarkReady()

	// Drain MarkReady notify
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Invalid action should trigger reconcile and still notify
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "invalid_action",
		DeltaUsers:  []model.UserSpec{{ID: 2, UUID: "u2"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify on failed delta apply")
	}

	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected NeedsReconcile for invalid delta action")
	}
}

func TestNodeMailboxRemovingLastUserPreservesEmptySnapshot(t *testing.T) {
	mb := NewNodeMailbox()
	user := model.UserSpec{ID: 1}
	mb.SeedBaseline([]model.UserSpec{user}, nil)
	mb.MarkReady()
	for range 2 {
		mb.Apply(Event{Type: EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: []model.UserSpec{user}})
		state := mb.DrainIfReady()
		if !state.HasUsers || state.Users == nil || len(state.Users) != 0 || state.NeedsReconcile {
			t.Fatalf("删除最后一个用户后应返回非 nil 空快照: %+v", state)
		}
		if mb.DrainIfReady().HasUsers {
			t.Fatal("已消费的用户快照被重复返回")
		}
	}
	mb.Apply(Event{Type: EventSyncUserDelta, DeltaAction: "add", DeltaUsers: []model.UserSpec{user}})
	state := mb.DrainIfReady()
	if !state.HasUsers || len(state.Users) != 1 || state.Users[0].ID != user.ID {
		t.Fatal("空快照之后未能重新添加用户")
	}
}
