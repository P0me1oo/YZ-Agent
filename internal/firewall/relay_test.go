package firewall

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

func verifiedLanding() *model.NodeSpec {
	node := landingWithSources("192.0.2.1", "192.0.2.2", "2001:db8::1", "2001:db8::2")
	node.Relay.EntryNodeID = 0 // 前置池没有单一入口编号。
	node.Relay.Firewall.Version = 1
	node.Relay.Firewall.ValidUntil = time.Now().Add(time.Minute).Unix()
	return node
}

func blockedBy(plan Plan, raw string) bool {
	ip := netip.MustParseAddr(raw)
	for _, raw := range plan.Blocked {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return true
		}
	}
	return false
}

func TestRelayPolicyCoversPoolAndBothFamiliesOnlyOnLandingPorts(t *testing.T) {
	for _, kernel := range []string{"xray", "singbox"} {
		t.Run(kernel, func(t *testing.T) {
			node := verifiedLanding()
			node.ListenIP = "0.0.0.0"
			plan, err := PlanForNode(node, kernel)
			if err != nil || !plan.Restricted || len(plan.Listeners) != 4 {
				t.Fatalf("%+v %v", plan, err)
			}
			for _, source := range node.Relay.Firewall.Sources {
				if blockedBy(plan, source) {
					t.Fatal("pool member blocked", source)
				}
			}
			for _, source := range []string{"192.0.2.3", "2001:db8::3"} {
				if !blockedBy(plan, source) {
					t.Fatal("unrelated source allowed", source)
				}
			}
			for _, rule := range plan.Listeners {
				if rule.Ports.From != 28388 || rule.Ports.To != 28388 {
					t.Fatal("unrelated port affected")
				}
			}
			node.Relay.Firewall.Sources = []string{"192.0.2.1"}
			plan, err = PlanForNode(node, kernel)
			if err != nil || !blockedBy(plan, "2001:db8::1") || blockedBy(plan, "192.0.2.1") {
				t.Fatal("IPv6 not closed", err)
			}
		})
	}
}

func TestRelayPolicyKeepsExplicitSourceRestrictions(t *testing.T) {
	node := verifiedLanding()
	node.SourcePolicy = &sourcepolicy.Policy{Mode: "blacklist", BlockIPs: []string{"192.0.2.1"}}
	node.SourcePolicyReady = true
	node.SourceBlockCIDRs = []string{"192.0.2.1/32"}
	plan, err := PlanForNode(node, "xray")
	if err != nil || !blockedBy(plan, "192.0.2.1") || blockedBy(plan, "192.0.2.2") {
		t.Fatal("manual source policy widened", err)
	}
}

type relayBackend struct {
	recordingBackend
	plans map[string]Plan
}

func (b *relayBackend) ApplySources(_ context.Context, plans map[string]Plan) error {
	if b.err != nil {
		return b.err
	}
	b.plans = plans
	return nil
}

func TestRelayPendingInvalidExpiredAndFailedUpdateKeepPreviousRules(t *testing.T) {
	ctx := context.Background()
	b := &relayBackend{}
	m := testManager(b)
	node := verifiedLanding()
	if err := m.Apply(ctx, "landing", node, "xray"); err != nil {
		t.Fatal(err)
	}
	original := b.plans["landing"]
	for _, test := range []struct {
		name   string
		policy *panel.RelayFirewallConfig
	}{
		{"pending", &panel.RelayFirewallConfig{Version: 1, Status: "pending"}},
		{"expired", &panel.RelayFirewallConfig{Version: 1, Status: "ready", Sources: []string{"192.0.2.2"}, ValidUntil: time.Now().Add(-time.Second).Unix()}},
		{"prefix rejected", &panel.RelayFirewallConfig{Version: 1, Status: "ready", Sources: []string{"192.0.2.0/24"}, ValidUntil: time.Now().Add(time.Minute).Unix()}},
		{"unknown protocol version", &panel.RelayFirewallConfig{Version: 99, Status: "ready"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			node.Relay.Firewall = test.policy
			if err := m.Apply(ctx, "landing", node, "xray"); err == nil {
				t.Fatal("invalid policy accepted")
			}
			if !reflect.DeepEqual(original, b.plans["landing"]) {
				t.Fatal("old rules changed")
			}
		})
	}
	other := &model.NodeSpec{Protocol: "vless", ServerPort: 28389}
	if err := m.Apply(ctx, "unrelated", other, "xray"); err != nil {
		t.Fatal("pending landing blocked unrelated port", err)
	}
	if !reflect.DeepEqual(original, b.plans["landing"]) {
		t.Fatal("unrelated update changed held policy")
	}
	b.err = errors.New("permission denied")
	node = verifiedLanding()
	node.Relay.Firewall.Sources = []string{"192.0.2.9"}
	if err := m.Apply(ctx, "landing", node, "xray"); err == nil {
		t.Fatal("write failure lost")
	}
	if !reflect.DeepEqual(original, m.plans["landing"]) {
		t.Fatal("failed candidate retained for background application")
	}
	b.err = nil
	if err := m.Apply(ctx, "landing", node, "xray"); err != nil {
		t.Fatal(err)
	}
	if blockedBy(b.plans["landing"], "192.0.2.9") || !blockedBy(b.plans["landing"], "192.0.2.1") {
		t.Fatal("recovery did not replace sources")
	}
	if err := m.Release(ctx, "landing"); err != nil || len(b.allows) != 2 {
		t.Fatal("unbind did not clean owned rules", err)
	}
}

func TestRelayPortConflictDoesNotAlterOrdinaryNode(t *testing.T) {
	b := &relayBackend{}
	m := testManager(b)
	ordinary := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 28388}
	if err := m.Apply(context.Background(), "ordinary", ordinary, "xray"); err != nil {
		t.Fatal(err)
	}
	before := append([]Rule{}, b.allows...)
	if err := m.Apply(context.Background(), "landing", verifiedLanding(), "xray"); err == nil {
		t.Fatal("shared port accepted")
	}
	if !reflect.DeepEqual(before, b.allows) {
		t.Fatal("unrelated rules changed")
	}
}
