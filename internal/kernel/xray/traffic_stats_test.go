package xray

import (
	"fmt"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/infra/conf/serial"
	"strings"
)

func TestAgentStatsModuleIsUsedAndHistoricalNativeCountersRetire(t *testing.T) {
	decoded, err := serial.DecodeJSONConfig(strings.NewReader(`{"stats":{},"log":{"loglevel":"none"},"outbounds":[{"protocol":"freedom"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := decoded.Build()
	if err != nil {
		t.Fatal(err)
	}
	useTrafficStats(pb)
	instance, err := xrayCore.New(pb)
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	manager, ok := instance.GetFeature(stats.ManagerType()).(*trafficStats)
	if !ok {
		t.Fatal("核心未接入 Agent 统计模块")
	}
	x := New(config.KernelConfig{})
	x.instance, x.nodeConfig = instance, &model.NodeSpec{Protocol: "vless"}
	for id := 1; id <= 10000; id++ {
		counter, err := manager.GetOrRegisterCounter(fmt.Sprintf("user>>>user@%d>>>traffic>>>uplink", id))
		if err != nil {
			t.Fatal(err)
		}
		counter.Add(100)
		x.collectStatsLocked()
		removed := x.RetireTraffic(model.TrafficCounters{Users: map[int][2]int64{id: {100, 0}}})
		if len(removed.Users) != 1 || len(manager.counters) != 0 || len(manager.users) != 0 {
			t.Fatal("历史原生计数仍长期保留")
		}
		// 已回收的旧引用迟到写入，与同用户重新加入共享新计数组。
		fresh, err := manager.GetOrRegisterCounter(fmt.Sprintf("user>>>user@%d>>>traffic>>>uplink", id))
		if err != nil {
			t.Fatal(err)
		}
		fresh.Add(7)
		counter.Add(3)
		x.collectStatsLocked()
		if x.cumTraffic[id][0] != 10 {
			t.Fatal("原生计数回收丢失或重复迟到流量")
		}
		x.RetireTraffic(model.TrafficCounters{Users: map[int][2]int64{id: {10, 0}}})
	}
}

func TestNativeStatsRetirementCancelsWhenUnsampledBytesRemain(t *testing.T) {
	manager, err := newTrafficStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	name := "user>>>user@1>>>traffic>>>uplink"
	counter, _ := manager.GetOrRegisterCounter(name)
	counter.Add(1)
	if manager.retireUser(1) || manager.GetCounter(name) == nil || counter.Value() != 1 {
		t.Fatal("尚未采样字节被清空")
	}
}

func TestNativeStatsConcurrentRetirementKeepsEveryByte(t *testing.T) {
	manager, err := newTrafficStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	counter, _ := manager.GetOrRegisterCounter("user>>>user@1>>>traffic>>>uplink")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				counter.Add(1)
			}
		})
	}
	var collected int64
	collect := func() {
		manager.VisitCounters(func(_ string, counter stats.Counter) bool { collected += counter.Set(0); return true })
	}
	for range 100 {
		collect()
		manager.retireUser(1)
	}
	wg.Wait()
	collect()
	if collected != 4000 {
		t.Fatalf("并发原生回收计数=%d", collected)
	}
}
