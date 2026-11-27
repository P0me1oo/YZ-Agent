package service

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestRelayEgressRequiresCompleteDualStackRoute(t *testing.T) {
	for _, test := range []struct {
		name                    string
		failed, custom, private bool
	}{
		{name: "both families"}, {name: "IPv6 route failed", failed: true},
		{name: "custom outbound", custom: true}, {name: "NAT source", private: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := &model.NodeSpec{RelayEgressTargets: []panel.RelayEgressTarget{
				{NodeID: 1, Address: "landing.invalid", Port: 443, Token: strings.Repeat("a", 64)},
			}}
			if test.custom {
				node.CustomOutbounds = []model.OutboundConfig{{Tag: "custom"}}
			}
			lookup := func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}, nil
			}
			route := func(_ context.Context, addr netip.Addr, _ int) (string, error) {
				if addr.Is6() && test.failed {
					return "", errors.New("no IPv6 route")
				}
				if test.private {
					return "10.0.0.1", nil
				}
				return addr.String(), nil
			}
			report := collectRelayEgress(context.Background(), node, lookup, route)
			complete := !test.failed && !test.custom && !test.private
			if len(report.Children) != 1 || report.Children[0].Complete != complete {
				t.Fatalf("%+v", report)
			}
			if complete && len(report.Children[0].Sources) != 2 {
				t.Fatal("lost address family")
			}
			if !complete && len(report.Children[0].Sources) != 0 {
				t.Fatal("partial measurement published")
			}
		})
	}
}

func TestPoolLandingFirewallFailureDoesNotRestartOrRelease(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	fw := &lifecycleFirewall{kernel: k}
	s.SetFirewallController(fw)
	s.lastConfig = landingConfig()
	s.lastConfig.Relay.EntryNodeID = 0
	s.lastConfig.Relay.Firewall = &panel.RelayFirewallConfig{Version: 1, Status: "ready", Sources: []string{"192.0.2.1"}, ValidUntil: time.Now().Add(time.Minute).Unix()}
	if !s.applyChanges(context.Background(), true, false) || !k.running {
		t.Fatal("start failed")
	}
	config := *s.lastConfig
	relay := *config.Relay
	relay.Firewall = &panel.RelayFirewallConfig{Version: 1, Status: "pending"}
	config.Relay = &relay
	fw.err = errors.New("cannot verify full pool")
	if !s.applyConfigUpdate(context.Background(), &config, computeConfigHash(&config)) {
		t.Fatal("update rejected")
	}
	if !k.running || fw.releases != 0 || k.reloadCalls != 0 || s.firewallNotice.Load() == nil {
		t.Fatal("failed policy changed runtime")
	}
	fw.err = nil
	if !s.applyFirewall(context.Background(), &config, nil) || s.firewallNotice.Load() != nil {
		t.Fatal("recovery not reported")
	}
}

func TestRelayMeasurementTargetChangesUpdateSnapshotWithoutKernelReload(t *testing.T) {
	before := &model.NodeSpec{Protocol: "vless", ServerPort: 443,
		RelayEgressTargets: []panel.RelayEgressTarget{{NodeID: 1, Address: "old.invalid", Port: 443, Token: strings.Repeat("a", 64)}}}
	after := *before
	after.RelayEgressTargets = []panel.RelayEgressTarget{{NodeID: 1, Address: "new.invalid", Port: 443, Token: strings.Repeat("b", 64)}}
	if computeConfigHash(before) == computeConfigHash(&after) {
		t.Fatal("target-only update would be discarded")
	}
	if computeKernelConfigHash(before) != computeKernelConfigHash(&after) {
		t.Fatal("target-only update would restart core")
	}
}

func TestNextRoutingExitDoesNotDisableRelaySourceMeasurement(t *testing.T) {
	node := &model.NodeSpec{AccessPolicyVersion: 1, CustomOutbounds: []model.OutboundConfig{{Tag: "next-exit-1", Protocol: "socks"}}, RelayEgressTargets: []panel.RelayEgressTarget{{NodeID: 1, Address: "landing.example", Port: 443, Token: strings.Repeat("a", 64)}}}
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	route := func(context.Context, netip.Addr, int) (string, error) { return "1.1.1.1", nil }
	if !collectRelayEgress(t.Context(), node, lookup, route).Children[0].Complete {
		t.Fatal("independent exit prevented relay source confirmation")
	}
	node.CustomOutbounds[0].Tag = "direct"
	if collectRelayEgress(t.Context(), node, lookup, route).Children[0].Complete {
		t.Fatal("arbitrary override was trusted")
	}
}
