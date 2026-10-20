package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type firewallPolicyKernel struct {
	*fakeKernel
	prepared *model.NodeSpec
}

func (k *firewallPolicyKernel) Start(node *model.NodeSpec, users []model.UserSpec, cert kernel.TLSCert) error {
	k.prepared = node
	return k.fakeKernel.Start(node, users, cert)
}

func TestAutoFirewallSwitchWithdrawsBeforeListenAndRetainsCorePolicy(t *testing.T) {
	ctx := context.Background()
	k := &fakeKernel{}
	s := newTestService(k)
	pk := &firewallPolicyKernel{fakeKernel: k}
	s.kernel = pk
	fw := &lifecycleFirewall{kernel: k}
	s.SetFirewallController(fw)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 43222,
		SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}}
	s.lastUsers = []model.UserSpec{{ID: 1}}
	if !s.startKernel(ctx, s.lastConfig, s.lastUsers) {
		t.Fatal("自动管理未能启动")
	}
	beforeApplies, beforePrepares := len(fw.ports), len(fw.runningAtPrepare)
	off := *s.lastConfig
	off.AutoFirewall = new(false)
	if !s.applyConfigUpdate(ctx, &off, computeConfigHash(&off)) || !k.running || fw.releases == 0 {
		t.Fatal("关闭开关后未撤回规则并恢复监听")
	}
	if len(fw.ports) != beforeApplies || len(fw.runningAtPrepare) != beforePrepares || s.lastConfig.SourcePolicy == nil {
		t.Fatal("关闭后仍调用系统防火墙，或丢失核心来源策略")
	}
	if pk.prepared.ManagesFirewall() || !pk.prepared.SourcePolicyReady || len(pk.prepared.SourceBlockCIDRs) == 0 {
		t.Fatal("关闭系统管理后未把来源名单继续交给代理核心")
	}
	if !s.applyConfigUpdate(ctx, &off, computeConfigHash(&off)) || len(fw.ports) != beforeApplies {
		t.Fatal("重复关闭重新接管了端口")
	}
	on := off
	on.AutoFirewall = new(true)
	if !s.applyConfigUpdate(ctx, &on, computeConfigHash(&on)) || !k.running || len(fw.ports) <= beforeApplies || len(fw.runningAtPrepare) <= beforePrepares {
		t.Fatal("重新开启未先准备来源保护再接管端口")
	}
}

func TestAutoFirewallManualModeDoesNotRequireSystemController(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	pk := &firewallPolicyKernel{fakeKernel: k}
	s.kernel, s.firewall = pk, nil
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 43222, AutoFirewall: new(false),
		SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}}
	if !s.startKernel(context.Background(), node, []model.UserSpec{{ID: 1}}) || !k.running || !pk.prepared.SourcePolicyReady {
		t.Fatal("手动模式不应要求系统防火墙权限，核心仍须保留来源限制")
	}
}

func TestAutoFirewallWithdrawalFailureCannotPretendToBeManual(t *testing.T) {
	ctx := context.Background()
	k := &fakeKernel{}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k}
	s.SetFirewallController(fw)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 43222}
	s.lastUsers = []model.UserSpec{{ID: 1}}
	if !s.startKernel(ctx, s.lastConfig, s.lastUsers) {
		t.Fatal("初始接管失败")
	}
	off := *s.lastConfig
	off.AutoFirewall = new(false)
	fw.releaseErr = errors.New("遗留规则尚未完成撤回")
	if s.applyConfigUpdate(ctx, &off, computeConfigHash(&off)) || k.running || s.runtimeError == nil {
		t.Fatal("撤回失败时仍报告手动模式并启动了监听")
	}
	fw.releaseErr = nil
	s.lastFirewallRetry = time.Now().Add(-31 * time.Second)
	s.retrySourceFirewall(ctx)
	if !k.running || s.runtimeError != nil || len(fw.ports) != 1 {
		t.Fatal("撤回成功后未恢复手动模式，或重新接管了端口")
	}
}
