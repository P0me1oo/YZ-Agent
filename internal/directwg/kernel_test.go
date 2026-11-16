package directwg

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

type accountingKernel struct {
	kernel.Kernel
	traffic      map[int][2]int64
	alive        map[int]map[string]bool
	connections  int
	active       map[int]bool
	beforeRetire func()
}

func (k *accountingKernel) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	values := make(map[int][2]int64, len(k.traffic))
	for id, value := range k.traffic {
		values[id] = value
	}
	return values, k.alive, k.connections, nil
}

func (k *accountingKernel) RetireTraffic(candidates model.TrafficCounters) model.TrafficCounters {
	if k.beforeRetire != nil {
		k.beforeRetire()
	}
	removed := model.TrafficCounters{Users: make(map[int][2]int64)}
	for id, expected := range candidates.Users {
		if actual, exists := k.traffic[id]; exists && actual == expected && !k.active[id] {
			delete(k.traffic, id)
			removed.Users[id] = expected
		}
	}
	return removed
}

func TestManagedTrafficCombinesBothProtocolLedgers(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "proxy"
		if direct {
			name = "wireguard"
		}
		t.Run(name, func(t *testing.T) {
			base := &accountingKernel{
				traffic: map[int][2]int64{1: {100, 200}, 2: {30, 40}},
				alive:   map[int]map[string]bool{2: {"8.8.8.8": true}}, connections: 2,
			}
			state := newState()
			state.totals = map[int][2]int64{1: {50, 60}, 3: {70, 80}}
			managed := &Managed{Kernel: base, state: state, direct: direct}
			want := map[int][2]int64{1: {150, 260}, 2: {30, 40}, 3: {70, 80}}
			for range 2 {
				traffic, alive, count, err := managed.GetUserTraffic(t.Context())
				if err != nil || !reflect.DeepEqual(traffic, want) {
					t.Fatalf("协议切换丢失或重复累计: traffic=%v err=%v", traffic, err)
				}
				if direct && (len(alive) != 0 || count != 0) {
					t.Fatalf("WG 模式混入旧协议在线状态: alive=%v count=%d", alive, count)
				}
				if !direct && (!reflect.DeepEqual(alive, base.alive) || count != base.connections) {
					t.Fatal("当前代理协议在线状态丢失")
				}
			}
		})
	}
}

func TestManagedRetirementPreservesCombinedBaselineAndLateBytes(t *testing.T) {
	for _, source := range []string{"proxy", "wireguard", "both"} {
		t.Run(source, func(t *testing.T) {
			base := &accountingKernel{traffic: make(map[int][2]int64)}
			state := newState()
			var want [2]int64
			if source != "wireguard" {
				base.traffic[1] = [2]int64{100, 200}
				want = [2]int64{100, 200}
			}
			if source != "proxy" {
				state.totals[1] = [2]int64{50, 60}
				want[0] += 50
				want[1] += 60
			}
			managed := &Managed{Kernel: base, state: state, direct: true}
			accounting := tracker.New()
			values, _, _, _ := managed.GetUserTraffic(t.Context())
			accounting.Process(values, nil, 0)
			accounting.RetireConfirmed(managed.RetireTraffic)
			if len(base.traffic)+len(state.totals) == 0 {
				t.Fatal("未确认的累计被回收")
			}
			if got := accounting.FlushTraffic()[1]; got != want {
				t.Fatalf("两份账本结算错误: got=%v want=%v", got, want)
			}
			accounting.RetireConfirmed(managed.RetireTraffic)
			values, _, _, _ = managed.GetUserTraffic(t.Context())
			if len(values) != 0 {
				t.Fatalf("确认后历史账本未回收: %v", values)
			}
			// 回收后迟到的成功下载只能形成新增量。
			state.totals[1] = [2]int64{0, 7}
			values, _, _, _ = managed.GetUserTraffic(t.Context())
			accounting.Process(values, nil, 0)
			if got := accounting.FlushTraffic()[1]; got != [2]int64{0, 7} {
				t.Fatalf("迟到下载丢失或重复: %v", got)
			}
		})
	}
}

func TestManagedRetirementKeepsLiveOrChangedCounters(t *testing.T) {
	for _, change := range []string{"proxy-active", "wg-active", "proxy-new-bytes"} {
		t.Run(change, func(t *testing.T) {
			base := &accountingKernel{traffic: map[int][2]int64{1: {100, 200}}, active: make(map[int]bool)}
			state := newState()
			state.totals[1] = [2]int64{50, 60}
			switch change {
			case "proxy-active":
				base.active[1] = true
			case "wg-active":
				state.replaceUsers([]model.UserSpec{stateUser(t)})
			case "proxy-new-bytes":
				base.beforeRetire = func() { base.traffic[1] = [2]int64{103, 200} }
			}
			managed := &Managed{Kernel: base, state: state, direct: true}
			removed := managed.RetireTraffic(model.TrafficCounters{Users: map[int][2]int64{1: {150, 260}}})
			if len(removed.Users) != 0 || state.totals[1] != [2]int64{50, 60} {
				t.Fatalf("活跃或变化后的账本被部分回收: removed=%v wg=%v", removed, state.totals)
			}
			values, _, _, _ := managed.GetUserTraffic(t.Context())
			want := [2]int64{150, 260}
			if change == "proxy-new-bytes" {
				want[0] += 3
			}
			if values[1] != want {
				t.Fatalf("部分回收破坏累计基线: got=%v want=%v", values[1], want)
			}
		})
	}
}

func TestManagedConcurrentDownloadAndRetirementKeepsAllBytes(t *testing.T) {
	base := &accountingKernel{traffic: make(map[int][2]int64)}
	managed := &Managed{Kernel: base, state: newState(), direct: true}
	accounting := tracker.New()
	var writers sync.WaitGroup
	for range 4 {
		writers.Go(func() {
			for range 1000 {
				managed.state.recordDownload(1, 1)
			}
		})
	}
	var sum int64
	collect := func() {
		values, _, _, err := managed.GetUserTraffic(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		accounting.Process(values, nil, 0)
		sum += accounting.FlushTraffic()[1][1]
		accounting.RetireConfirmed(managed.RetireTraffic)
	}
	for range 100 {
		collect()
	}
	writers.Wait()
	collect()
	if sum != 4000 {
		t.Fatalf("并发写入与账本回收丢失或重复下载: %d", sum)
	}
}
