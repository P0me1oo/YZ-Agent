package service

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestMachineSnapshotAndPollingKeepUnchangedKernel(t *testing.T) {
	ctx := context.Background()
	k := &fakeKernel{running: true}
	s := newTestService(k)
	users := []model.UserSpec{{ID: 1, UUID: "00000000-0000-4000-8000-000000000001"}}
	input := &panel.NodeConfig{Protocol: "vless", ServerPort: 10001, ListenIP: "127.0.0.1", Network: "tcp"}
	httpConfig := model.NodeSpecFromPanel(input)
	s.lastConfig, s.lastConfigHash = httpConfig, computeConfigHash(httpConfig)
	s.updateUserState(users)
	version := panel.StateVersion{Epoch: "test-control", Sequence: 1}
	s.controlVersion.accept(version)
	s.machineMailbox = controlplane.NewNodeMailbox()
	s.machineMailbox.MarkReady()

	// 面板与用户均未改变，交替执行完整推送和定时拉取。
	for cycle := 0; cycle < 3; cycle++ {
		s.machineMailbox.Apply(controlplane.Event{
			Type: controlplane.EventSyncSnapshot, Version: version,
			Config: model.NodeSpecFromPanel(input), Users: users,
		})
		s.drainMachineMailbox(ctx)
		s.applyPullResult(ctx, pullResult{
			config: httpConfig, configHash: computeConfigHash(httpConfig),
			users: users, userHash: computeUserHash(users),
			controlVersion: version, generation: s.controlGeneration,
		})
	}
	if k.reloadCalls != 0 || k.startCalls != 0 {
		t.Fatalf("相同配置交替同步不应重建核心：重载 %d 次，启动 %d 次", k.reloadCalls, k.startCalls)
	}

	// 真正改变监听端口仍须应用，后续同配置拉取不能再重载。
	input.ServerPort++
	version.Sequence++
	s.machineMailbox.Apply(controlplane.Event{
		Type: controlplane.EventSyncSnapshot, Version: version,
		Config: model.NodeSpecFromPanel(input), Users: users,
	})
	s.drainMachineMailbox(ctx)
	httpConfig = model.NodeSpecFromPanel(input)
	s.applyPullResult(ctx, pullResult{
		config: httpConfig, configHash: computeConfigHash(httpConfig),
		users: users, userHash: computeUserHash(users),
		controlVersion: version, generation: s.controlGeneration,
	})
	if k.reloadCalls != 1 || s.lastConfig.ServerPort != input.ServerPort {
		t.Fatal("真正的配置变更必须正常触发一次重载")
	}
}
