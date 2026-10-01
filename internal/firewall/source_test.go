package firewall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

func landingWithSources(sources ...string) *model.NodeSpec {
	return &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 28388, Relay: &model.RelayConfig{
		Mode: "landing", Protocol: "shadowsocks", ListenPort: 28388, EntryNodeID: 1,
		Firewall: &panel.RelayFirewallConfig{Status: "ready", Sources: sources},
	}}
}

// 旧面板仍可能下发来源策略，新版必须忽略并恢复普通双栈放行。
func TestRelaySourcePolicyRemoved(t *testing.T) {
	for _, policy := range []*panel.RelayFirewallConfig{nil, {Status: "pending"}, {Status: "ready", Sources: []string{"192.0.2.1"}}} {
		for _, kernel := range []string{"xray", "singbox"} {
			node := landingWithSources()
			node.Relay.Firewall = policy
			plan, err := PlanForNode(node, kernel)
			if err != nil || plan.Restricted || hasSources(plan.Listeners) || len(plan.Listeners) != 4 {
				t.Fatalf("落地应普通双栈放行: %+v %v", plan, err)
			}
			b := &recordingBackend{}
			m := testManager(b)
			for i := 0; i < 2; i++ {
				if err := m.Apply(context.Background(), "landing", node, kernel); err != nil {
					t.Fatal(err)
				}
			}
			if len(b.allows) != 4 {
				t.Fatal("重复同步规则不正确")
			}
			if err := m.Release(context.Background(), "landing"); err != nil || len(b.allows) != 0 {
				t.Fatal("停用未清理规则")
			}
		}
	}
}

func TestUFWSourceRoundTripAndLegacyIdentity(t *testing.T) {
	b := &systemBackend{scope: "scope", state: savedState{Namespace: "yz-agent"}}
	rule := Rule{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 28388, To: 28388}}
	oldKey := rule.key()
	if oldKey != "4//tcp/28388/0" {
		t.Fatal("旧版规则标识改变")
	}
	rule.Source = "192.0.2.1"
	args := b.ufwArgs(rule, false)
	if !strings.Contains(strings.Join(args, " "), "from 192.0.2.1") {
		t.Fatal("UFW 来源丢失")
	}
	if !ufwAddedOwns("ufw "+strings.Join(args, " "), rule, b.ufwComment(rule)) {
		t.Fatal("停用状态无法回收来源规则")
	}
	parsed := parseUFWStatus("[ 1] 28388/tcp ALLOW IN 192.0.2.1 # " + b.ufwComment(rule))
	if len(parsed) != 1 || parsed[0].Rule != rule {
		t.Fatal("来源规则解析丢失")
	}
	if !strings.Contains(b.richRule(rule), `source address="192.0.2.1"`) {
		t.Fatal("firewalld 来源丢失")
	}
}

func TestUFWIPv6SourcesWithoutFamilyMarker(t *testing.T) {
	for _, line := range []string{
		"[ 1] 28388/tcp ALLOW IN 2001:db8::2 # owned",
		"[ 1] 28388/tcp ALLOW IN 2001:db8::2/128 # owned",
		"[ 1] ::/0 28388/tcp ALLOW IN 2001:db8::2 # owned",
	} {
		parsed := parseUFWStatus(line)
		if len(parsed) != 1 || parsed[0].ScopedSource || parsed[0].Rule.Family != 6 || parsed[0].Rule.Source != "2001:db8::2" {
			t.Fatalf("明确 IPv6 来源必须保持归属: %+v", parsed)
		}
	}
}

type runtimeFirewalldCommands struct {
	zone    string
	service string
}

func (runtimeFirewalldCommands) Available(string) bool { return true }
func (c runtimeFirewalldCommands) Run(_ context.Context, name, _ string, args ...string) (string, error) {
	if name == "firewall-cmd" && len(args) == 2 && args[0] == "--zone=public" && args[1] == "--list-all" {
		return c.zone, nil
	}
	if name == "firewall-cmd" && len(args) == 1 && args[0] == "--info-service=ssh" {
		return c.service, nil
	}
	return "", errors.New("测试仅允许查询 firewalld 当前运行配置")
}

func TestFirewalldRuntimeSourceConflicts(t *testing.T) {
	wanted := Rule{Family: 4, Source: "192.0.2.1", Protocol: "tcp", Ports: portset.Range{From: 28388, To: 28388}}
	for _, tc := range []struct {
		name, zone, service string
		blocked             bool
	}{
		{"普通区域", "public (active)\n  target: default\n  services: ssh\n  ports: 28444/tcp\n", "ssh\n  ports: 22/tcp\n  protocols:\n  source-ports:\n", false},
		{"默认全放行", "public\n  target: ACCEPT\n", "", true},
		{"未知策略", "public\n", "", true},
		{"公开端口", "public\n  target: default\n  ports: 28000-29000/tcp\n", "", true},
		{"整协议", "public\n  target: default\n  protocols: tcp\n", "", true},
		{"来源端口", "public\n  target: default\n  source-ports: 443/tcp\n", "", true},
		{"服务共用", "public\n  target: default\n  services: ssh\n", "ssh\n  ports: 28388/tcp\n", true},
		{"服务整协议", "public\n  target: default\n  services: ssh\n", "ssh\n  ports:\n  protocols: tcp\n", true},
		{"服务引用", "public\n  target: default\n  services: ssh\n", "ssh\n  ports:\n  includes: custom\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &systemBackend{commands: runtimeFirewalldCommands{zone: tc.zone, service: tc.service}}
			err := b.checkFirewalldSourceConflicts(context.Background(), "public", []Rule{wanted}, nil)
			var confirmation *ConfirmationRequired
			if tc.blocked && !errors.As(err, &confirmation) || !tc.blocked && err != nil {
				t.Fatalf("当前运行配置判断错误: %v", err)
			}
		})
	}
}

func TestExternalRulesAreCheckedBeforeSourceMutation(t *testing.T) {
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "scope", &commandSpy{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	wanted := Rule{Family: 4, Source: "192.0.2.1", Protocol: "tcp", Ports: portset.Range{From: 28388, To: 28388}}
	for _, line := range []string{
		"[ 1] 28000:29000/tcp ALLOW IN Anywhere # manual",
		"[ 1] 28388/tcp DENY IN Anywhere # manual",
		"[ 1] 28388/tcp ALLOW IN 192.0.2.2 # manual",
		"[ 1] Anywhere ALLOW IN 192.0.2.0/24 # manual",
		"[ 1] Custom-App ALLOW IN Anywhere # manual",
		"[ 1] 28388/tcp ALLOW IN 192.0.2.2 1024:65535 # manual",
		"[ 1] 192.0.2.0/24 28388/tcp ALLOW IN Anywhere # manual",
	} {
		if err := b.checkUFWSourceConflicts([]Rule{wanted}, parseUFWStatus(line)); err == nil {
			t.Fatal("外部共用规则没有阻止自动收紧")
		}
	}
	if !publicPortsOverlap("22/tcp 28000-29000/tcp", []Rule{wanted}) {
		t.Fatal("firewalld 范围共用没有被识别")
	}
	if richRuleMayOverlap(`rule family="ipv4" port port="443" protocol="tcp" accept`, wanted) {
		t.Fatal("无关端口不应冲突")
	}
}
