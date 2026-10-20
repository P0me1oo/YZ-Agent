package firewall

import (
	"context"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type manualPolicyUFW struct {
	*sourceUFW
	blocks string
}

func (r *manualPolicyUFW) Run(ctx context.Context, name, input string, args ...string) (string, error) {
	output, err := r.sourceUFW.Run(ctx, name, input, args...)
	if len(args) > 0 && args[0] == "status" {
		output += r.blocks
	}
	return output, err
}

func TestUFWManualPolicyReplacesOwnedRangeWithoutChangingWhitelist(t *testing.T) {
	ctx := context.Background()
	runner := &manualPolicyUFW{sourceUFW: &sourceUFW{rules: map[Rule]string{}}, blocks: "[ 9] 43222 DENY IN Anywhere\n"}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "owner", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	old := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43221, To: 43223}}
	if err := b.remember(ownedRule{Backend: "ufw", Rule: old}); err != nil {
		t.Fatal(err)
	}
	runner.rules[old] = b.ufwLegacyComment(old)
	whitelist := old
	whitelist.Source, whitelist.Ports = "198.51.100.7", portset.Range{From: 43222, To: 43222}
	runner.rules[whitelist] = "manual-whitelist"
	wanted := whitelist
	wanted.Source = ""
	// 即使其他节点尚未发现，也不能保留能覆盖手工限制的旧合并放行。
	b.recovering = true
	if err := b.Apply(ctx, []Rule{wanted}, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.rules) != 3 || runner.rules[whitelist] != "manual-whitelist" || runner.rules[old] != "" || runner.rules[wanted] != "" {
		t.Fatalf("手工白名单被改写，或旧自动放行仍覆盖受限端口: %v", runner.rules)
	}
	for _, port := range []int{43221, 43223} {
		neighbor := old
		neighbor.Ports = portset.Range{From: port, To: port}
		if runner.rules[neighbor] == "" {
			t.Fatal("相邻节点的既有放行丢失")
		}
	}
	writes := runner.writes
	if err := b.Apply(ctx, []Rule{wanted}, nil); err != nil || runner.writes != writes {
		t.Fatalf("重复同步应保持手工管理，不重复写入: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newSystemBackend(b.cfg, b.scope, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.recovering = true
	if err := reopened.Apply(ctx, []Rule{wanted}, nil); err != nil || runner.writes != writes {
		t.Fatalf("重启恢复不能重新公开手工受限端口: %v", err)
	}
	reopened.recovering = false
	if err := reopened.Apply(ctx, nil, nil); err != nil || len(runner.rules) != 1 || runner.rules[whitelist] != "manual-whitelist" {
		t.Fatalf("停止只应清理自动规则: %v", err)
	}
}

func TestUFWManualPolicySplitsMergedRangesAndKeepsOtherFamilies(t *testing.T) {
	allows := []Rule{
		{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 1000, To: 1010}},
		{Family: 6, Protocol: "tcp", Ports: portset.Range{From: 1000, To: 1010}},
	}
	existing := parseUFWStatus("[ 1] 1002:1004/tcp DENY IN Anywhere\n[ 2] 1004:1006/tcp LIMIT IN Anywhere\n")
	managed, manual := splitUFWManagedAllows(allows, existing)
	want := []Rule{
		{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 1000, To: 1001}},
		{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 1007, To: 1010}},
		allows[1],
	}
	if !reflect.DeepEqual(managed, want) || len(manual) != 1 || manual[0].Ports != (portset.Range{From: 1002, To: 1006}) {
		t.Fatalf("未准确保留非受限范围和另一地址族: managed=%v manual=%v", managed, manual)
	}
}

func TestUFWManualPolicyCannotBeBypassedByAutomaticPortHopping(t *testing.T) {
	runner := &blockingUFW{status: "Status: active\n[ 1] 20000:20100/udp DENY IN Anywhere\n"}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "owner", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	redirect := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 20000, To: 20100}, Target: 8443}
	allow := redirect
	allow.Target = 0
	if err := b.Apply(context.Background(), []Rule{allow}, []Rule{redirect}); err == nil || runner.writes != 0 {
		t.Fatalf("不能通过转发绕过手工拒绝: %v", err)
	}
}
