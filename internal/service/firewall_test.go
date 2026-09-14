package service

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type lifecycleFirewall struct {
	kernel   *fakeKernel
	ports    []int
	releases int
	err      error
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
