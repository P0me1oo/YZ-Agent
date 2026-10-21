package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

type crashTestKernel struct {
	*fakeKernel
	name string
}

func (k *crashTestKernel) Name() string { return k.name }

func crashTestService(t *testing.T, name string) (*Service, *fakeKernel, *[]nlog.Event) {
	t.Helper()
	k := &fakeKernel{}
	s := newTestService(k)
	s.kernel = &crashTestKernel{fakeKernel: k, name: name}
	s.cfg.Panel.NodeID = 7
	s.cfg.InstanceID = t.Name()
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 18443}
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "crash-test-user"}}
	events := new([]nlog.Event)
	stop := nlog.Subscribe(s.cfg.LogScope(), func(event nlog.Event) {
		if event.Event != "" {
			*events = append(*events, event)
		}
	})
	t.Cleanup(stop)
	return s, k, events
}

func TestXrayCrashStartReloadRecoveryAndDuplicateFailure(t *testing.T) {
	s, k, events := crashTestService(t, "xray")
	k.startErr = errors.New("listener failed password=temporary-test-secret")
	ctx := context.Background()
	if s.applyChanges(ctx, true, false) || k.running {
		t.Fatal("启动失败不能报告运行")
	}
	s.applyChanges(ctx, true, false)
	if len(*events) != 1 {
		t.Fatal("未恢复的故障重复通知")
	}
	event := (*events)[0]
	if event.NodeID != 7 || event.Event != "xray.crash" || event.Source != "xray" || event.Level != "error" || event.OccurredAt == 0 || strings.Contains(event.Message, "temporary-test-secret") {
		t.Fatal("故障事件身份、时间或敏感内容处理错误")
	}
	k.startErr = nil
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 21443}
	if !s.applyChanges(ctx, true, false) || !k.running {
		t.Fatal("修正配置后必须恢复")
	}
	k.reloadErr = errors.New("reload failed")
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 22443}
	if s.applyChanges(ctx, true, false) || k.running {
		t.Fatal("重载失败必须停止")
	}
	if len(*events) != 2 || !strings.Contains((*events)[1].Message, "重载内核失败") {
		t.Fatal("恢复后的新故障未通知")
	}
}

func TestXrayCrashUnexpectedStopAndNormalStop(t *testing.T) {
	s, k, events := crashTestService(t, "xray")
	ctx := context.Background()
	if !s.applyChanges(ctx, true, false) {
		t.Fatal("启动失败")
	}
	k.running = false
	s.trackAndEnforce(ctx)
	s.trackAndEnforce(ctx)
	if len(*events) != 1 || !strings.Contains((*events)[0].Message, "内核意外停止") {
		t.Fatal("意外停止应通知一次")
	}
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 21443}
	if !s.applyChanges(ctx, true, false) {
		t.Fatal("恢复失败")
	}
	s.lastUsers = nil
	s.applyChanges(ctx, true, false)
	s.trackAndEnforce(ctx)
	if len(*events) != 1 {
		t.Fatal("正常停止不能通知")
	}
}

func TestXrayCrashIgnoresOtherKernelAndPreparationErrors(t *testing.T) {
	for _, name := range []string{"xray", "singbox"} {
		t.Run(name, func(t *testing.T) {
			s, _, events := crashTestService(t, name)
			s.failRuntime("应用防火墙规则失败", s.lastConfig, s.lastUsers, errors.New("test failure"))
			s.failRuntime("配置校验失败", s.lastConfig, s.lastUsers, errors.New("test failure"))
			if len(*events) != 0 {
				t.Fatal("准备配置失败不能冒充核心崩溃")
			}
			s.failRuntime("启动内核失败", s.lastConfig, s.lastUsers, errors.New("test failure"))
			expected := 0
			if name == "xray" {
				expected = 1
			}
			if len(*events) != expected {
				t.Fatal("错误的核心通知范围")
			}
		})
	}
}

func TestXrayCrashDoesNotAlertWhenLastUserRemovalStopsKernel(t *testing.T) {
	s, k, events := crashTestService(t, "xray")
	ctx := context.Background()
	if !s.applyChanges(ctx, true, false) {
		t.Fatal("启动失败")
	}
	k.onRemoveUsers = func([]model.UserSpec) { k.running = false }
	if !s.applyUserDelta(ctx, "remove", s.lastUsers) {
		t.Fatal("删除用户失败")
	}
	s.trackAndEnforce(ctx)
	if len(*events) != 0 || s.runtimeError != nil {
		t.Fatal("删除最后一个用户后停止不能视为崩溃")
	}
}
