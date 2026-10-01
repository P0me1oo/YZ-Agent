package firewall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type sourceUFW struct {
	rules   map[Rule]string
	writes  int
	failAdd bool
}

func (*sourceUFW) Available(name string) bool { return name == "ufw" }

func (r *sourceUFW) Run(_ context.Context, name, _ string, args ...string) (string, error) {
	if name != "ufw" {
		return "", fmt.Errorf("意外命令: %s", name)
	}
	if args[0] == "status" {
		output := "Status: active\nDefault: deny (incoming), allow (outgoing)\n"
		for rule, comment := range r.rules {
			source := rule.Source
			if source == "" {
				source = "Anywhere"
			}
			if rule.Family == 6 && rule.Source == "" {
				source += " (v6)"
			}
			output += fmt.Sprintf("[ 1] %s %s/%s ALLOW IN %s # %s\n", rule.Address, strings.ReplaceAll(rule.Ports.String(), "-", ":"), rule.Protocol, source, comment)
		}
		return output, nil
	}
	remove := args[0] == "--force"
	if !remove && r.failAdd {
		return "", errors.New("模拟写入失败")
	}
	var rule Rule
	var comment string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "proto":
			rule.Protocol = args[i+1]
		case "from":
			value := args[i+1]
			rule.Family = 4
			if strings.Contains(value, ":") {
				rule.Family = 6
			}
			if value != "0.0.0.0/0" && value != "::/0" {
				rule.Source = value
			}
		case "to":
			if args[i+1] != "0.0.0.0/0" && args[i+1] != "::/0" {
				rule.Address = args[i+1]
			}
		case "port":
			ports, err := portset.Parse(strings.ReplaceAll(args[i+1], ":", "-"))
			if err != nil || len(ports) != 1 {
				return "", err
			}
			rule.Ports = ports[0]
		case "comment":
			comment = args[i+1]
		}
	}
	if !rule.valid() {
		return "", fmt.Errorf("测试命令不完整: %v", args)
	}
	r.writes++
	if remove {
		delete(r.rules, rule)
	} else {
		r.rules[rule] = comment
	}
	return "", nil
}

func seedSourceBackend(t *testing.T) (*systemBackend, *sourceUFW, Rule) {
	t.Helper()
	runner := &sourceUFW{rules: map[Rule]string{}}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "source-owner", runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	old := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 28388, To: 28388}}
	for _, family := range []int{4, 6} {
		rule := old
		rule.Family = family
		if err := b.remember(ownedRule{Backend: "ufw", Rule: rule}); err != nil {
			t.Fatal(err)
		}
		runner.rules[rule] = b.ufwComment(rule)
	}
	return b, runner, old
}

func TestSourceMigrationReplacesLegacyWildcardAndSurvivesRestart(t *testing.T) {
	b, runner, old := seedSourceBackend(t)
	wanted := old
	wanted.Source = "192.0.2.1"
	if err := b.Apply(context.Background(), []Rule{wanted}, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.rules) != 1 || runner.rules[wanted] == "" || len(b.state.Owned) != 1 || b.state.Version != 2 {
		t.Fatal("没有替换两种地址族的全开放托管规则")
	}
	b.Close()
	reopened, err := newSystemBackend(b.cfg, b.scope, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	writes := runner.writes
	if err := reopened.Apply(context.Background(), []Rule{wanted}, nil); err != nil || runner.writes != writes {
		t.Fatalf("重启恢复不能重复加规则: %v", err)
	}
	wanted.Source = "192.0.2.2"
	if err := reopened.Apply(context.Background(), []Rule{wanted}, nil); err != nil {
		t.Fatal(err)
	}
	if len(runner.rules) != 1 || runner.rules[wanted] == "" {
		t.Fatal("IP 变化后旧地址没有撤销")
	}
	if err := reopened.Apply(context.Background(), nil, nil); err != nil || len(runner.rules) != 0 || reopened.state.Version != 1 {
		t.Fatalf("正常清理后应恢复旧程序可读取的空状态: %v", err)
	}
}

func TestSourceFailureAndPendingRecoveryNeverRemoveOldRules(t *testing.T) {
	b, runner, old := seedSourceBackend(t)
	b.recovering = true
	if err := b.Apply(context.Background(), nil, nil); err != nil || len(runner.rules) != 2 {
		t.Fatal("节点尚未发现就回收旧规则")
	}
	b.recovering = false
	b.protected = []Rule{old, {Family: 6, Protocol: "tcp", Ports: old.Ports}}
	if err := b.Apply(context.Background(), nil, nil); err != nil || runner.writes != 0 {
		t.Fatal("待确认期间改变规则")
	}
	b.protected = nil
	runner.failAdd = true
	wanted := old
	wanted.Source = "192.0.2.1"
	if err := b.Apply(context.Background(), []Rule{wanted}, nil); err == nil || len(runner.rules) != 2 {
		t.Fatal("新规则写入失败时删除旧规则")
	}
	runner.failAdd = false
	if err := b.Apply(context.Background(), []Rule{wanted}, nil); err != nil || len(runner.rules) != 1 {
		t.Fatal("恢复后没有完成替换")
	}
}

func TestSourceRemovalRestoresPortsAndSurvivesRestart(t *testing.T) {
	b, runner, old := seedSourceBackend(t)
	source := old
	source.Source = "192.0.2.1"
	if err := b.Apply(context.Background(), []Rule{source}, nil); err != nil {
		t.Fatal(err)
	}
	b.Close()
	reopened, err := newSystemBackend(b.cfg, b.scope, runner)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	m := testManager(reopened)
	m.BeginDiscovery([]string{"instance"})
	ExpectNodes(m, "instance", []int{1, 2})
	node := landingWithSources()
	node.Protocol, node.Relay.Protocol = "vless", "vless"
	runner.failAdd = true
	if err := m.Apply(context.Background(), "instance/node/1", node, "xray"); err == nil || runner.rules[source] == "" {
		t.Fatal("放行失败必须保留旧来源规则")
	}
	runner.failAdd = false
	if err := m.Apply(context.Background(), "instance/node/1", node, "xray"); err != nil {
		t.Fatal(err)
	}
	if runner.rules[source] == "" {
		t.Fatal("发现尚未完成就删除旧规则")
	}
	if err := m.Release(context.Background(), "instance/node/2"); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.rules) != 2 || runner.rules[source] != "" || reopened.state.Version != 1 {
		t.Fatal("未将旧来源规则替换为双栈普通放行")
	}
	writes := runner.writes
	if err := m.Apply(context.Background(), "instance/node/1", node, "xray"); err != nil || writes != runner.writes {
		t.Fatal("重复同步改写规则")
	}
	if err := m.Release(context.Background(), "instance/node/1"); err != nil || len(runner.rules) != 0 {
		t.Fatal("停止后未清理")
	}
}

func TestPendingLegacyIPv6RangeCannotLeaveLandingOpen(t *testing.T) {
	b, runner, old := seedSourceBackend(t)
	b.state.Owned = nil
	runner.rules = map[Rule]string{}
	old.Family, old.Ports.To = 6, old.Ports.To+1
	if err := b.remember(ownedRule{Backend: "ufw", Rule: old}); err != nil {
		t.Fatal(err)
	}
	runner.rules[old] = b.ufwComment(old)
	pendingPort := old
	pendingPort.Ports.From = old.Ports.To
	b.protected = []Rule{pendingPort}
	wanted := old
	wanted.Family, wanted.Source, wanted.Ports.To = 4, "192.0.2.1", old.Ports.From
	var confirmation *ConfirmationRequired
	if err := b.Apply(context.Background(), []Rule{wanted}, nil); !errors.As(err, &confirmation) || runner.writes != 0 {
		t.Fatalf("IPv6 旧全开放范围尚在时不能声称 IPv4 来源策略已生效: %v", err)
	}
	b.protected = nil
	if err := b.Apply(context.Background(), []Rule{wanted}, nil); err != nil || len(runner.rules) != 1 || runner.rules[wanted] == "" {
		t.Fatalf("解除待确认后应撤销旧 IPv6 范围: %v", err)
	}
}

func TestPendingKeepsPreviouslyConfirmedDualStackSources(t *testing.T) {
	b, runner, old := seedSourceBackend(t)
	first, second, ipv6 := old, old, old
	first.Source, second.Source = "192.0.2.1", "192.0.2.2"
	ipv6.Family, ipv6.Source = 6, "2001:db8::1"
	allows := []Rule{first, second, ipv6}
	if err := b.Apply(context.Background(), allows, nil); err != nil {
		t.Fatal(err)
	}
	b.protected = []Rule{old, {Family: 6, Protocol: old.Protocol, Ports: old.Ports}}
	writes := runner.writes
	if err := b.Apply(context.Background(), allows, nil); err != nil || runner.writes != writes {
		t.Fatalf("待确认时应完整保留已有双栈多来源规则: %v", err)
	}
}
