package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestUserSpeedUsesActualIntervalAndKeepsPublishedSnapshot(t *testing.T) {
	var sampler userSpeedSampler
	now := time.Now()
	totals := map[int][2]int64{1: {1000000, 2000000}}
	sampler.sample(totals, now)
	if sampler.rates != nil {
		t.Fatal("首次采样不应把累计流量当作网速")
	}
	totals[1] = [2]int64{1000300, 2000600}
	totals[2] = [2]int64{150, 0}
	sampler.sample(totals, now.Add(1500*time.Millisecond))
	want := map[int][2]int64{1: {200, 400}, 2: {100, 0}}
	if !reflect.DeepEqual(sampler.rates, want) {
		t.Fatalf("实际间隔或新用户计数错误: %v", sampler.rates)
	}
	published := sampler.rates
	sampler.sample(totals, now.Add(2500*time.Millisecond))
	if sampler.rates == nil || len(sampler.rates) != 0 || !reflect.DeepEqual(published, want) {
		t.Fatal("无流量应发送空快照，且不能修改已发送快照")
	}
}

func TestUserSpeedRebaselinesAfterGapOrCounterReset(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		gap   time.Duration
		total int64
	}{
		{"采样中断", 6 * time.Second, 1000000},
		{"计数归零", time.Second, 5},
		{"重复时间", 0, 150},
		{"时钟回退", -time.Second, 150},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var sampler userSpeedSampler
			now := time.Now()
			sampler.sample(map[int][2]int64{1: {100, 100}}, now)
			now = now.Add(scenario.gap)
			sampler.sample(map[int][2]int64{1: {scenario.total, scenario.total}}, now)
			if sampler.rates != nil {
				t.Fatal("无效采样应显示未知")
			}
			sampler.sample(map[int][2]int64{1: {scenario.total + 200, scenario.total + 400}}, now.Add(time.Second))
			if sampler.rates[1] != [2]int64{200, 400} {
				t.Fatal("重新建立基准后未恢复")
			}
		})
	}
}

type speedTestKernel struct {
	*fakeKernel
	totals map[int][2]int64
	err    error
}

func (k *speedTestKernel) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	return k.totals, nil, 0, k.err
}

type speedTestPublisher struct {
	*shutdownReportPlane
	states chan panel.StatePayload
}

func (p *speedTestPublisher) RealtimeEnabled() bool { return true }
func (p *speedTestPublisher) PublishState(_ context.Context, state panel.StatePayload) error {
	p.states <- state
	return nil
}

func TestUserSpeedStateDoesNotDrainBillingAndFailureClearsBaseline(t *testing.T) {
	s, cp := newShutdownReportService()
	k := &speedTestKernel{fakeKernel: &fakeKernel{}, totals: map[int][2]int64{1: {100, 200}}}
	s.kernel = k
	p := &speedTestPublisher{shutdownReportPlane: cp, states: make(chan panel.StatePayload, 1)}
	s.sink = p
	ctx := context.Background()
	if _, _, err := s.collectTraffic(ctx); err != nil {
		t.Fatal(err)
	}
	s.userSpeed.at = time.Now().Add(-time.Second)
	k.totals = map[int][2]int64{1: {400, 800}}
	if _, _, err := s.collectTraffic(ctx); err != nil {
		t.Fatal(err)
	}
	s.publishRuntimeState(ctx)
	select {
	case state := <-p.states:
		if state.UserSpeeds[1][0] <= 0 || state.UserSpeeds[1][1] <= 0 {
			t.Fatalf("网速未传入状态: %v", state.UserSpeeds)
		}
	case <-time.After(time.Second):
		t.Fatal("没有发送实时状态")
	}
	if got := s.tracker.FlushTraffic()[1]; got != [2]int64{400, 800} {
		t.Fatalf("网速采样改变了计费: %v", got)
	}
	k.err = errors.New("测试采样失败")
	if _, _, err := s.collectTraffic(ctx); err == nil || s.userSpeed.rates != nil {
		t.Fatal("失败未清理基准")
	}
	k.err = nil
	k.totals = map[int][2]int64{1: {1000, 2000}}
	if _, _, err := s.collectTraffic(ctx); err != nil {
		t.Fatal(err)
	}
	if s.userSpeed.rates != nil {
		t.Fatal("恢复后的首次采样不能显示补报速度")
	}
	if got := s.tracker.FlushTraffic()[1]; got != [2]int64{600, 1200} {
		t.Fatalf("失败恢复丢失计费流量: %v", got)
	}
}
