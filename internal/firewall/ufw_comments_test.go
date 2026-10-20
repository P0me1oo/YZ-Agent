package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

func TestUFWNodeCommentsTrackSharingAndSurviveRestart(t *testing.T) {
	ctx := context.Background()
	runner := &sourceUFW{rules: map[Rule]string{}}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	m := testManager(b)
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 43222, ListenIP: "0.0.0.0"}
	rule := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}}
	check := func(label string) {
		t.Helper()
		if len(runner.rules) != 1 || !strings.HasPrefix(runner.rules[rule], "yz-agent:"+label+":test-owner:") {
			t.Fatalf("节点注释或共享规则数量不正确: %v", runner.rules)
		}
	}
	if err := m.Apply(ctx, "instance/node/123", node, "xray"); err != nil {
		t.Fatal(err)
	}
	check("node=123")
	writes := runner.writes
	if err := m.Apply(ctx, "instance/node/123", node, "xray"); err != nil || runner.writes != writes {
		t.Fatalf("重复同步不应改写注释: %v", err)
	}
	if err := m.Apply(ctx, "instance/node/7", node, "xray"); err != nil {
		t.Fatal(err)
	}
	check("nodes=7,123")
	if err := m.Release(ctx, "instance/node/123"); err != nil {
		t.Fatal(err)
	}
	check("node=7")
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newSystemBackend(b.cfg, b.scope, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// 重启时尚未拿到节点计划，也必须能回收带节点列表的旧注释。
	if err := reopened.Apply(ctx, nil, nil); err != nil || len(runner.rules) != 0 || len(reopened.state.Owned) != 0 {
		t.Fatalf("重启后未精确清理带节点标记的规则: %v", err)
	}
}

func TestUFWNodeCommentsCoverMergedRangesAndPortHopping(t *testing.T) {
	b := &systemBackend{scope: "test-owner", state: savedState{Namespace: "yz-agent"}, plans: map[string]Plan{
		"first/node/123": {Listeners: []Rule{{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}}, Redirects: []Rule{{Family: 4, Protocol: "udp", Ports: portset.Range{From: 20000, To: 20100}, Target: 8443}}},
		"first/node/7":   {Listeners: []Rule{{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8444, To: 8444}}}},
		"other/node/123": {Listeners: []Rule{{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}}},
	}}
	for _, test := range []struct {
		ports portset.Range
		label string
	}{
		{portset.Range{From: 8443, To: 8444}, "nodes=7,123"},
		{portset.Range{From: 20000, To: 20100}, "node=123"},
	} {
		rule := Rule{Family: 4, Protocol: "udp", Ports: test.ports}
		if !strings.HasPrefix(b.ufwComment(rule), "yz-agent:"+test.label+":") {
			t.Fatalf("合并范围、去重或跳跃端口丢失节点: %s", b.ufwComment(rule))
		}
	}
}

func TestUFWLegacyCommentUpdatesInPlaceAndRetriesFailure(t *testing.T) {
	b, runner, rule := seedSourceBackend(t)
	legacy := runner.rules[rule]
	plan := Plan{Listeners: []Rule{rule}}
	b.plans = map[string]Plan{"instance/node/123": plan}
	runner.failAdd = true
	if err := b.Apply(context.Background(), []Rule{rule}, nil); err == nil || runner.rules[rule] != legacy {
		t.Fatal("注释更新失败必须保留原放行和归属")
	}
	runner.failAdd = false
	if err := b.Apply(context.Background(), []Rule{rule}, nil); err != nil || !strings.Contains(runner.rules[rule], ":node=123:") {
		t.Fatalf("旧注释未原位迁移: %v", err)
	}
	writes := runner.writes
	if err := b.Apply(context.Background(), []Rule{rule}, nil); err != nil || runner.writes != writes {
		t.Fatalf("迁移后重复同步不应写入: %v", err)
	}
}

func TestUFWNodeCommentsPreserveManualAndForeignRules(t *testing.T) {
	rule := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}}
	for _, foreign := range []bool{false, true} {
		runner := &sourceUFW{rules: map[Rule]string{rule: "manual"}}
		b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "owner", runner)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.Close() })
		b.plans = map[string]Plan{"instance/node/123": {Listeners: []Rule{rule}}}
		if foreign {
			other := &systemBackend{scope: "other", state: savedState{Namespace: "yz-agent"}, plans: b.plans}
			runner.rules[rule] = other.ufwComment(rule)
		}
		err = b.Apply(context.Background(), []Rule{rule}, nil)
		if (err != nil) != foreign || runner.writes != 0 || len(b.state.Owned) != 0 {
			t.Fatalf("不得改写、认领手工或其他实例规则: %v", err)
		}
	}
}

func TestUFWCommentOwnershipRequiresExactInstanceRuleAndValidNodeList(t *testing.T) {
	b := &systemBackend{scope: "owner", state: savedState{Namespace: "yz-agent"}}
	rule := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}}
	legacy := b.ufwLegacyComment(rule)
	_, suffix, _ := strings.Cut(legacy, ":")
	for _, test := range []struct {
		label string
		owned bool
	}{
		{"node=123", true}, {"nodes=7,123", true},
		{"node=0", false}, {"node=-1", false}, {"node=01", false},
		{"node=abc", false}, {"node=7,123", false}, {"nodes=123", false},
		{"nodes=123,7", false}, {"nodes=7,7", false}, {"nodes=7,", false},
	} {
		comment := "yz-agent:" + test.label + ":" + suffix
		if b.ownsUFWComment(rule, comment) != test.owned {
			t.Fatalf("错误识别节点标记: %s", comment)
		}
	}
	if !b.ownsUFWComment(rule, legacy) || b.ownsUFWComment(rule, strings.Replace(legacy, ":owner:", ":other:", 1)) {
		t.Fatal("旧格式或实例隔离识别错误")
	}
	otherRule := rule
	otherRule.Ports.To++
	if b.ownsUFWComment(otherRule, legacy) {
		t.Fatal("不能把相邻端口规则认领为当前规则")
	}
}

func TestUFWInactiveCleanupRecognizesNodeCommentsAndRetainsFailedIntent(t *testing.T) {
	spy := &commandSpy{err: errors.New("模拟删除失败")}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "owner", spy)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rule := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}}
	owned := ownedRule{Backend: "ufw", Rule: rule}
	if err := b.remember(owned); err != nil {
		t.Fatal(err)
	}
	b.plans = map[string]Plan{"instance/node/123": {Listeners: []Rule{rule}}}
	args := b.ufwArgs(rule, false)
	oldComment := b.ufwComment(rule)
	added := "ufw " + strings.Join(args[:len(args)-1], " ") + " '" + oldComment + "'"
	b.plans = nil
	if err := b.removeUFW(context.Background(), owned, nil, added); err == nil || !b.hasOwned(owned) {
		t.Fatal("清理失败必须保留归属供重试")
	}
	spy.err = nil
	if err := b.removeUFW(context.Background(), owned, nil, added); err != nil || b.hasOwned(owned) {
		t.Fatalf("未清理停用防火墙中的旧节点注释: %v", err)
	}
	if len(spy.calls) != 2 || spy.calls[1][len(spy.calls[1])-1] != oldComment {
		t.Fatal("删除必须使用实际旧注释")
	}
}
