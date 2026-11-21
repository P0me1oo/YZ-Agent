package xray

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestRelayAccountingIncludesDirectAndIgnoresUnrelatedOutbounds(t *testing.T) {
	x, manager := newStatsBackedXray(t, []model.UserSpec{{ID: 1}}, nil)
	x.nodeConfig = trafficRelayConfig(7)
	x.nodeConfig.Relay.RouteID = 11
	x.nodeConfig.Relay.Accounting = "routes"
	mustRegisterCounter(t, manager, "user>>>user@1>>>relay>>>11>>>traffic>>>uplink").Add(31)
	mustRegisterCounter(t, manager, "user>>>user@1>>>relay>>>3>>>traffic>>>uplink").Add(47)
	mustRegisterCounter(t, manager, "outbound>>>direct>>>traffic>>>uplink").Add(99)
	mustRegisterCounter(t, manager, "outbound>>>relay-line>>>traffic>>>uplink").Add(47)
	for range 2 {
		u, err := x.GetRelayUserTraffic(context.Background())
		if err != nil || u[1][0][0] != 31 || u[1][7][0] != 47 {
			t.Fatalf("routes %v: %v", u, err)
		}
		loads, _ := x.GetRelayTraffic(context.Background())
		if len(loads) != 1 || loads[7][0] != 47 {
			t.Fatalf("loads %v", loads)
		}
	}
}
