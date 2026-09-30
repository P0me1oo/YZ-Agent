package firewall

import (
	"context"
	"errors"
	"reflect"
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

func TestRelaySourcePolicyScopeAndFamilies(t *testing.T) {
	node := landingWithSources("192.0.2.1", "2001:db8::1", "192.0.2.1")
	plan, err := PlanForNode(node, "xray")
	if err != nil || len(plan.Listeners) != 4 {
		t.Fatalf("来源计划错误: %+v %v", plan, err)
	}
	for _, rule := range plan.Listeners {
		if rule.Source == "" || rule.Ports.From != 28388 || !rule.valid() {
			t.Fatal("来源限制或内部端口丢失")
		}
	}
	node.Relay.EntryNodeID = 0
	plain, err := PlanForNode(node, "singbox")
	if err != nil || hasSources(plain.Listeners) {
		t.Fatal("未绑定前置的独立监听不应被收紧")
	}
	node.Relay.Mode = "entry"
	plain, err = PlanForNode(node, "singbox")
	if err != nil || hasSources(plain.Listeners) {
		t.Fatal("前置公开入口不应被收紧")
	}
}

func TestRelayUnknownSourceAndSharedPortsPreserveExistingRules(t *testing.T) {
	b := &recordingBackend{}
	m := testManager(b)
	ctx := context.Background()
	node := landingWithSources("192.0.2.1")
	if err := m.Apply(ctx, "landing", node, "xray"); err != nil {
		t.Fatal(err)
	}
	previous := append([]Rule(nil), b.allows...)
	node.Relay.Firewall = nil
	var pendingError *ConfirmationRequired
	if err := m.Apply(ctx, "landing", node, "xray"); !errors.As(err, &pendingError) {
		t.Fatalf("缺地址必须待确认: %v", err)
	}
	if !reflect.DeepEqual(previous, b.allows) {
		t.Fatal("缺地址不能删除旧规则")
	}
	other := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 28388}
	if err := m.Apply(ctx, "public", other, "xray"); !errors.As(err, &pendingError) {
		t.Fatalf("共用端口必须待确认: %v", err)
	}
	if !reflect.DeepEqual(previous, b.allows) {
		t.Fatal("共用不能全开放")
	}
	if err := m.Release(ctx, "public"); err != nil {
		t.Fatal(err)
	}
	node.Relay.Firewall = &panel.RelayFirewallConfig{Status: "ready", Sources: []string{"192.0.2.2"}}
	if err := m.Apply(ctx, "landing", node, "xray"); err != nil {
		t.Fatal(err)
	}
	for _, rule := range b.allows {
		if rule.Source != "192.0.2.2" {
			t.Fatal("旧出口没有撤销")
		}
	}
	if err := m.Apply(ctx, "landing", node, "xray"); err != nil || len(b.allows) != 2 {
		t.Fatal("重复同步不应重复添加")
	}
}

func TestRelayInvalidSourcesCannotBecomeWildcard(t *testing.T) {
	for _, source := range []string{"", "0.0.0.0", "::", "192.0.2.0/24", "127.0.0.1", "example.com", "ff02::1", "fe80::1%eth0"} {
		_, err := PlanForNode(landingWithSources(source), "singbox")
		var confirmation *ConfirmationRequired
		if !errors.As(err, &confirmation) {
			t.Fatalf("%q 必须待确认: %v", source, err)
		}
	}
	plan, err := PlanForNode(landingWithSources("10.0.0.8"), "xray")
	if err != nil || len(plan.Listeners) != 2 || plan.Listeners[0].Source != "10.0.0.8" {
		t.Fatal("面板明确确认的内网中转来源应使用具体 IP 放行")
	}
}

func TestNewPendingLandingCannotBeOpenedBySharedPublicNode(t *testing.T) {
	b := &recordingBackend{}
	m := testManager(b)
	node := landingWithSources()
	node.Relay.Firewall = nil
	var confirmation *ConfirmationRequired
	if err := m.Apply(context.Background(), "landing", node, "xray"); !errors.As(err, &confirmation) {
		t.Fatalf("没有来源时应等待确认: %v", err)
	}
	public := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: node.ServerPort}
	if err := m.Apply(context.Background(), "public", public, "xray"); !errors.As(err, &confirmation) {
		t.Fatalf("共用节点不能在落地待确认时创建全开放规则: %v", err)
	}
	if len(b.allows) != 0 {
		t.Fatal("尚无旧规则的待确认落地被共用节点全开放")
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
