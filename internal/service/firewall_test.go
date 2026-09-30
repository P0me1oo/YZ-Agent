package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type lifecycleFirewall struct {
	kernel   *fakeKernel
	ports    []int
	releases int
	err      error
}

func TestRelayFirewallConfirmationKeepsRuntimeAndUpdatesWithoutReload(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k, err: &firewall.ConfirmationRequired{Message: "待确认"}}
	s.SetFirewallController(fw)
	s.lastConfig = landingConfig()
	s.lastConfig.Relay.EntryNodeID = 1
	if !s.applyChanges(context.Background(), true, false) || !k.running || fw.releases != 0 || s.firewallNotice.Load() == nil {
		t.Fatal("待确认错误不应停止中转或删除已有规则")
	}
	config := landingConfig()
	config.Relay.EntryNodeID = 1
	config.Relay.Firewall = &panel.RelayFirewallConfig{Status: "ready", Sources: []string{"192.0.2.1"}}
	fw.err = nil
	if !s.applyConfigUpdate(context.Background(), config, computeConfigHash(config)) || k.reloadCalls != 0 || s.firewallNotice.Load() != nil {
		t.Fatal("仅来源变化不应重建监听，成功后应清除告警")
	}
	fw.err = errors.New("模拟系统规则写入失败")
	config.Relay.Firewall.Sources = []string{"192.0.2.2"}
	if !s.applyFirewall(context.Background(), config, nil) || !k.running || fw.releases != 0 {
		t.Fatal("落地新规则写入失败不能回收旧规则")
	}
}

func TestRelayFirewallReportsLaterConflictAndRecoveryWithoutConfigChange(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k}
	s.SetFirewallController(fw)
	s.lastConfig = landingConfig()
	s.lastConfig.Relay.EntryNodeID = 1
	if !s.applyChanges(context.Background(), true, false) || s.firewallNotice.Load() != nil {
		t.Fatal("初始来源规则未成功应用")
	}
	fw.err = &firewall.ConfirmationRequired{Message: "新增手工规则与落地共用端口"}
	s.trackAndEnforce(context.Background())
	if s.firewallNotice.Load() == nil || !k.running || fw.releases != 0 {
		t.Fatal("运行后新增冲突必须回报告警并保留监听")
	}
	checks := len(fw.ports)
	s.trackAndEnforce(context.Background())
	if len(fw.ports) != checks {
		t.Fatal("每次流量采样不应重复检查防火墙")
	}
	fw.err = nil
	s.lastFirewallRetry = time.Now().Add(-31 * time.Second)
	s.trackAndEnforce(context.Background())
	if s.firewallNotice.Load() != nil || k.reloadCalls != 0 {
		t.Fatal("冲突消除后应清除提示且不重载监听")
	}
}

func (f *lifecycleFirewall) Apply(_ context.Context, _ string, node *model.NodeSpec, _ string) error {
	if !f.kernel.IsRunning() {
		return errors.New("不能在内核启动前开放端口")
	}
	f.ports = append(f.ports, node.ServerPort)
	return f.err
}

func (f *lifecycleFirewall) Release(ctx context.Context, _ string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.kernel.IsRunning() {
		return errors.New("不能在内核停止前回收规则")
	}
	f.releases++
	return nil
}

func TestFirewallTracksStartupReloadFailureAndRecovery(t *testing.T) {
	ctx := context.Background()
	k := &fakeKernel{}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k}
	s.SetFirewallController(fw)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 18443}
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "firewall-test-user"}}
	if !s.applyChanges(ctx, true, false) || len(fw.ports) != 1 {
		t.Fatal("成功启动后未申请端口")
	}
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 19443}
	if !s.applyChanges(ctx, true, false) || fw.ports[len(fw.ports)-1] != 19443 {
		t.Fatal("重载后未同步新端口")
	}
	k.reloadErr = errors.New("监听失败")
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 20443}
	if s.applyChanges(ctx, true, false) || k.running || fw.releases != 1 {
		t.Fatal("重载失败后未停止监听并回收规则")
	}
	k.reloadErr = nil
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 21443}
	if !s.applyChanges(ctx, true, false) || !k.running {
		t.Fatal("修正配置后无法恢复")
	}
	s.stopKernel()
	s.stopKernel()
	if fw.releases != 3 {
		t.Fatal("停止流程遗漏防火墙清理")
	}
}

func TestFirewallFailureCannotReportRunning(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k, err: errors.New("规则无法写入")}
	s.SetFirewallController(fw)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 18443}
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "firewall-test-user"}}
	var status RuntimeStatus
	s.SetStatusHandler(func(value RuntimeStatus) { status = value })
	if s.applyChanges(context.Background(), true, false) {
		t.Fatal("规则失败不能视为启动成功")
	}
	if k.running || status != RuntimeFailed || s.appliedState.Config != nil || fw.releases != 1 {
		t.Fatal("防火墙失败后仍保留运行状态或没有回收部分规则")
	}
}
