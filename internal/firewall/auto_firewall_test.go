package firewall

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type preparingBackend struct {
	recordingBackend
	restricted int
}

func (b *preparingBackend) ApplySources(_ context.Context, plans map[string]Plan) error {
	b.restricted = 0
	for _, plan := range plans {
		if plan.Restricted {
			b.restricted++
		}
	}
	return nil
}

func TestAutoFirewallPreparesProtectionBeforePortTakeover(t *testing.T) {
	ctx := context.Background()
	b := &preparingBackend{}
	m := testManager(b)
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 43222, SourcePolicyReady: true,
		SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}, SourceBlockCIDRs: []string{"203.0.113.0/24"}}
	if err := m.Prepare(ctx, "panel/node/123", node, "xray"); err != nil || b.restricted != 1 || len(b.allows) != 0 {
		t.Fatalf("监听前只能建立来源过滤，不能接管放行: %v", err)
	}
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil || len(b.allows) != 2 || b.restricted != 1 {
		t.Fatalf("监听确认后未完成端口接管: %v", err)
	}
	node.AutoFirewall = new(false)
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil || len(b.allows) != 0 || b.restricted != 0 {
		t.Fatalf("关闭开关未撤回放行和来源过滤: %v", err)
	}
}

func TestAutoFirewallOptOutRetainsOtherSharedOwner(t *testing.T) {
	b := &recordingBackend{}
	m := testManager(b)
	ctx := context.Background()
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 43222}
	for _, owner := range []string{"panel/node/1", "panel/node/2"} {
		if err := m.Apply(ctx, owner, node, "xray"); err != nil {
			t.Fatal(err)
		}
	}
	node.AutoFirewall = new(false)
	if err := m.Apply(ctx, "panel/node/1", node, "xray"); err != nil || len(b.allows) != 2 {
		t.Fatal("关闭一个节点误撤回其他节点仍需的共享规则")
	}
	if err := m.Apply(ctx, "panel/node/2", node, "xray"); err != nil || len(b.allows) != 0 {
		t.Fatal("最后一个使用者关闭后未回收规则")
	}
}

func TestAutoFirewallUFWTakesOverAndWithdrawsWithoutEditingManualRules(t *testing.T) {
	runner := &manualPolicyUFW{sourceUFW: &sourceUFW{rules: map[Rule]string{}}, blocks: "[ 9] 43222 DENY IN Anywhere\n"}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "managed", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	m := testManager(b)
	ctx := context.Background()
	manual := Rule{Family: 4, Protocol: "tcp", Source: "198.51.100.7", Ports: portset.Range{From: 43222, To: 43222}}
	runner.rules[manual] = "manual-whitelist"
	node := &model.NodeSpec{Protocol: "vless", ListenIP: "0.0.0.0", ServerPort: 43222}
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil {
		t.Fatal(err)
	}
	if len(runner.rules) != 2 || runner.rules[manual] != "manual-whitelist" || b.state.Version != 4 {
		t.Fatal("接管改写了原规则或未持久化可恢复的归属")
	}
	writes := runner.writes
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil || runner.writes != writes {
		t.Fatalf("重复同步重复接管: %v", err)
	}
	node.AutoFirewall = new(false)
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil || len(runner.rules) != 1 || runner.rules[manual] != "manual-whitelist" || b.state.Version != 1 {
		t.Fatalf("关闭开关未恢复原手工规则: %v", err)
	}
	writes = runner.writes
	if err := m.Apply(ctx, "panel/node/123", node, "xray"); err != nil || runner.writes != writes {
		t.Fatal("手动关闭后仍在修改防火墙")
	}
}

func TestAutoFirewallOptOutWaitsForRecoveryAndClearsLegacyRules(t *testing.T) {
	b, runner, _ := seedSourceBackend(t)
	m := testManager(b)
	m.BeginDiscovery([]string{"panel"})
	ExpectNodes(m, "panel", []int{1, 2})
	manual := &model.NodeSpec{Protocol: "vless", ServerPort: 28388, AutoFirewall: new(false)}
	var pending *ConfirmationRequired
	if err := m.Apply(context.Background(), "panel/node/1", manual, "xray"); !errors.As(err, &pending) || len(runner.rules) != 2 {
		t.Fatalf("归属尚未确认时不能谎报已完成撤回或删除其他节点规则: %v", err)
	}
	if err := m.Release(context.Background(), "panel/node/2"); err != nil || len(runner.rules) != 0 {
		t.Fatalf("发现完成后未撤回没有运行计划的遗留规则: %v", err)
	}
	if err := m.Apply(context.Background(), "panel/node/1", manual, "xray"); err != nil {
		t.Fatal(err)
	}
}
