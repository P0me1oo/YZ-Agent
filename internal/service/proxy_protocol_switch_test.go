package service

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestProxyProtocolSwitchConfigReloadAndFailureRecovery(t *testing.T) {
	base := &model.NodeSpec{Protocol: "vless", ServerPort: 443, AcceptProxyProtocol: new(false)}
	on := *base
	on.AcceptProxyProtocol = new(true)
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig, s.lastConfigHash = base, computeConfigHash(base)
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "proxy-switch-test"}}
	otherKernel := &fakeKernel{running: true}
	other := newTestService(otherKernel)
	other.lastConfig, other.lastConfigHash = base, computeConfigHash(base)
	event := controlplane.Event{Type: controlplane.EventSyncConfig, Config: &on}
	s.handleWSEvent(context.Background(), event)
	if s.lastConfigHash != computeConfigHash(&on) || k.reloadCalls != 1 {
		t.Fatal("开启开关未触发重载")
	}
	calls := k.reloadCalls
	s.handleWSEvent(context.Background(), event)
	if k.reloadCalls != calls {
		t.Fatal("重复推送导致重复重载")
	}
	if other.lastConfigHash != computeConfigHash(base) || otherKernel.reloadCalls != 0 {
		t.Fatal("推送影响其他节点")
	}
	s.handleWSEvent(context.Background(), controlplane.Event{Type: controlplane.EventSyncConfig, Config: base})
	if s.lastConfigHash != computeConfigHash(base) || k.reloadCalls != calls+1 {
		t.Fatal("关闭开关没有应用")
	}
	k.reloadErr = errors.New("测试重载失败")
	s.handleWSEvent(context.Background(), event)
	if s.runtimeError == nil || k.running {
		t.Fatal("重载失败后应记录错误并停止节点")
	}
	calls = k.reloadCalls
	s.handleWSEvent(context.Background(), event)
	if k.reloadCalls != calls || k.startCalls != 0 {
		t.Fatal("重复失败配置不应绕过既有停止保护")
	}
	k.reloadErr = nil
	s.handleWSEvent(context.Background(), controlplane.Event{Type: controlplane.EventSyncConfig, Config: base})
	if !k.running || k.startCalls != 1 || s.runtimeError != nil {
		t.Fatal("修正开关配置后未能恢复运行")
	}
}
