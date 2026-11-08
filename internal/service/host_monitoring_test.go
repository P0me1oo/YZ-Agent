package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type hostPolicyPlane struct {
	*speedTestPublisher
	enabled atomic.Bool
}

func (p *hostPolicyPlane) HostMonitoringEnabled() bool { return p.enabled.Load() }

func TestLitePolicyKeepsNodeBusinessAcrossAllReportingPaths(t *testing.T) {
	s, cp := newShutdownReportService()
	p := &hostPolicyPlane{speedTestPublisher: &speedTestPublisher{shutdownReportPlane: cp, states: make(chan panel.StatePayload, 1)}}
	s.source, s.sink = p, p
	s.kernel = &fakeKernel{running: true}
	s.operations.snapshot.Store(&operationsSnapshot{Version: 1})
	s.tracker.Process(map[int][2]int64{1: {100, 200}}, map[int]map[string]bool{1: {"8.8.8.8": true}}, 3)
	s.tracker.ProcessConnectionCounts(map[int]int{1: 3}, nil)
	s.tracker.ProcessIPConnectionCounts(map[int]map[string]int{1: {"8.8.8.8": 3}})
	s.userSpeed.rates = map[int][2]int64{1: {10, 20}}
	for _, enabled := range []bool{false, true, false} {
		p.enabled.Store(enabled)
		if s.collectStatus().HostSkipped == enabled {
			t.Fatal("采样策略未生效")
		}
		metrics := s.wsMetrics()
		if (metrics["load"] != nil) != enabled || metrics["kernel_status"] != true || metrics["active_connections"] != 3 {
			t.Fatal("旧 WebSocket 指标没有保留代理状态或仍携带主机采样")
		}
		s.publishRuntimeState(context.Background())
		select {
		case state := <-p.states:
			if (state.Status["mem"] != nil) != enabled || (state.Metrics["load"] != nil) != enabled {
				t.Fatal("实时负载没有随策略切换")
			}
			if state.Status["kernel_status"] != true || state.Operations == nil || state.ConnectionCounts[1] != 3 ||
				state.IPConnectionCounts[1]["8.8.8.8"] != 3 || state.UserSpeeds[1] != [2]int64{10, 20} {
				t.Fatal("实时状态丢失代理业务")
			}
		case <-time.After(time.Second):
			t.Fatal("停止主机监控时不能停止业务上报")
		}
		deadline := time.Now().Add(time.Second)
		for s.stateActive.Load() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if s.stateActive.Load() {
			t.Fatal("上报没有结束")
		}
	}
	batch := s.takeReportBatch()
	if batch.payload.Traffic[1] != [2]int64{100, 200} || batch.payload.ConnectionCounts[1] != 3 ||
		batch.payload.Metrics["kernel_status"] != true || batch.payload.Mem != [2]uint64{} || batch.payload.Metrics["load"] != nil {
		t.Fatal("兼容批次丢失代理业务或携带主机负载")
	}
}
