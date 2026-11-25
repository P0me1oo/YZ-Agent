package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type demandTestPublisher struct {
	*speedTestPublisher
	details atomic.Bool
	speeds  atomic.Bool
}

func (p *demandTestPublisher) TelemetryNeeds() (bool, bool) { return p.details.Load(), p.speeds.Load() }

func TestTelemetrySubscriptionKeepsBusinessStateAndRestoresDisplay(t *testing.T) {
	s, cp := newShutdownReportService()
	s.kernel = &speedTestKernel{fakeKernel: &fakeKernel{}, totals: map[int][2]int64{1: {100, 200}}}
	p := &demandTestPublisher{speedTestPublisher: &speedTestPublisher{shutdownReportPlane: cp, states: make(chan panel.StatePayload, 1)}}
	s.sink = p
	s.tracker.ProcessConnectionCounts(map[int]int{}, map[int]map[int]int{})
	recordShutdownTraffic(s, 100)
	s.userSpeed.rates = map[int][2]int64{1: {10, 20}}
	s.relayEgress.Store(&relayEgress{Version: 1, CheckedAt: time.Now().Unix(), Children: []relayEgressChild{
		{NodeID: 2, Token: "measurement", Complete: true, Sources: []string{"8.8.8.8"}},
	}})
	read := func() panel.StatePayload {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for s.stateActive.Load() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		s.publishRuntimeState(context.Background())
		select {
		case state := <-p.states:
			return state
		case <-time.After(time.Second):
			t.Fatal("没有发送必要状态")
			return panel.StatePayload{}
		}
	}
	state := read()
	if state.Status != nil || state.Metrics != nil || state.UserSpeeds != nil {
		t.Fatal("无订阅时仍发送展示数据")
	}
	if state.RelayEgress == nil {
		t.Fatal("来源测量不应受展示数据订阅影响")
	}
	if state.Alive == nil || state.Online == nil || state.ConnectionCounts == nil {
		t.Fatal("必要空快照丢失")
	}
	p.details.Store(true)
	p.speeds.Store(true)
	state = read()
	if state.Status == nil || state.Metrics == nil || state.UserSpeeds[1] != [2]int64{10, 20} {
		t.Fatal("打开页面后未恢复完整展示")
	}
	if state.Metrics["relay_egress"] == nil || state.RelayEgress == nil {
		t.Fatal("旧面板指标测量与 Next 实时测量必须同时兼容")
	}
	p.details.Store(false)
	p.speeds.Store(false)
	state = read()
	if state.Status != nil || state.UserSpeeds != nil {
		t.Fatal("关闭订阅后未停止展示上报")
	}
	if got := s.tracker.FlushTraffic()[1]; got != [2]int64{100, 100} {
		t.Fatalf("切换展示上报改变了计费流量：%v", got)
	}
}
