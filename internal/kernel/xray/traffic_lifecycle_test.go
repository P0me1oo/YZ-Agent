package xray

import (
	"context"
	"fmt"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/xtls/xray-core/features/stats"
)

func TestXrayTrafficKeepsRemovedUsersAndRetiredRelayMappings(t *testing.T) {
	ctx := context.Background()
	users := []model.UserSpec{{ID: 1}}
	x, oldStats := newStatsBackedXray(t, users, nil)
	x.nodeConfig = trafficRelayConfig(11)
	oldCounters := registerLifecycleCounters(t, oldStats)
	addLifecycleTraffic(oldCounters, 100)
	assertXrayTraffic(t, x, 100, map[int]int64{11: 100})

	previous := x.retireCurrentLocked()
	next, newStats := newStatsBackedXray(t, users, nil)
	x.instance = next.instance
	x.nodeConfig = trafficRelayConfig(12)
	x.users = nil
	newCounters := registerLifecycleCounters(t, newStats)
	addLifecycleTraffic(oldCounters, 25)
	addLifecycleTraffic(newCounters, 200)
	for range 2 {
		assertXrayTraffic(t, x, 325, map[int]int64{11: 125, 12: 200})
	}

	// 回收前最后一笔必须归属旧逻辑节点；清理旧实例不能清空累计值。
	addLifecycleTraffic(oldCounters, 10)
	x.recycleOld(previous)
	if len(x.retired) != 0 {
		t.Fatal("已回收的实例仍留在排空集合")
	}
	x.nodeConfig = nil
	assertXrayTraffic(t, x, 335, map[int]int64{11: 135, 12: 200})
	x.Stop()
	assertXrayTraffic(t, x, 335, map[int]int64{11: 135, 12: 200})

	traffic, _, _, _ := x.GetUserTraffic(ctx)
	traffic[1] = [2]int64{}
	relay, _ := x.GetRelayTraffic(ctx)
	relay[11] = [2]int64{}
	relayUser, _ := x.GetRelayUserTraffic(ctx)
	relayUser[1][11] = [2]int64{}
	assertXrayTraffic(t, x, 335, map[int]int64{11: 135, 12: 200})
}

func trafficRelayConfig(nodeID int) *model.NodeSpec {
	return &model.NodeSpec{Protocol: "vless", Relay: &model.RelayConfig{
		Mode:     panel.RelayModeEntry,
		Children: []model.RelayChild{{NodeID: nodeID, RouteID: 3, Tag: "relay-line"}},
	}}
}

func registerLifecycleCounters(t *testing.T, manager stats.Manager) []stats.Counter {
	t.Helper()
	var counters []stats.Counter
	for _, direction := range []string{"uplink", "downlink"} {
		for _, name := range []string{
			counterName(1, direction),
			fmt.Sprintf("outbound>>>relay-line>>>traffic>>>%s", direction),
			fmt.Sprintf("user>>>user@1>>>relay>>>3>>>traffic>>>%s", direction),
		} {
			counters = append(counters, mustRegisterCounter(t, manager, name))
		}
	}
	return counters
}

func addLifecycleTraffic(counters []stats.Counter, value int64) {
	for _, counter := range counters {
		counter.Add(value)
	}
}

func assertXrayTraffic(t *testing.T, x *Xray, userTotal int64, relayTotals map[int]int64) {
	t.Helper()
	traffic, _, _, err := x.GetUserTraffic(context.Background())
	if err != nil || traffic[1] != [2]int64{userTotal, userTotal} {
		t.Fatalf("累计用户流量错误: %v, err=%v", traffic, err)
	}
	relay, err := x.GetRelayTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	relayUser, err := x.GetRelayUserTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for nodeID, total := range relayTotals {
		want := [2]int64{total, total}
		if relay[nodeID] != want || relayUser[1][nodeID] != want {
			t.Fatalf("逻辑节点 %d 的累计流量错误: relay=%v, user=%v", nodeID, relay[nodeID], relayUser[1][nodeID])
		}
	}
}
