package service

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/monitor"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type discoveryResult struct {
	push        controlplane.PushClient
	err         error
	wasRealtime bool
}

type versionGate struct{ panel.VersionGate }

func (g *versionGate) accept(version panel.StateVersion) bool {
	return g.Accept(version)
}

func (s *Service) realtimeEnabled() bool {
	publisher, ok := s.sink.(controlplane.StatePublisher)
	return ok && publisher.RealtimeEnabled()
}

// publishRuntimeState 不阻塞主循环。慢通道期间不堆积采样，下一次发送直接取最新快照。
func (s *Service) publishRuntimeState(ctx context.Context) {
	publisher, ok := s.sink.(controlplane.StatePublisher)
	if !ok || !publisher.RealtimeEnabled() || !s.stateActive.CompareAndSwap(false, true) {
		return
	}
	status := monitor.Collect()
	counts, relayCounts := s.tracker.CurrentConnectionCounts()
	metrics := s.buildMetrics(status)
	metrics["kernel_status"] = s.kernel.IsRunning()
	state := panel.StatePayload{
		Alive: s.tracker.FlushAliveIPs(), Online: s.tracker.CurrentOnline(),
		ConnectionCounts: counts, RelayConnectionCounts: relayCounts,
		UserSpeeds:     s.userSpeed.rates,
		RelayUserAlive: s.tracker.RelayUserAlive(), Metrics: metrics,
		Status: map[string]interface{}{
			"cpu":           status.CPU,
			"mem":           map[string]uint64{"total": status.MemTotal, "used": status.MemUsed},
			"swap":          map[string]uint64{"total": status.SwapTotal, "used": status.SwapUsed},
			"disk":          map[string]uint64{"total": status.DiskTotal, "used": status.DiskUsed},
			"kernel_status": s.kernel.IsRunning(),
		},
	}
	go func() {
		defer s.stateActive.Store(false)
		reportCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if err := publisher.PublishState(reportCtx, state); err != nil && ctx.Err() == nil {
			nlog.Core().Warn("runtime state report failed", "error", err)
		}
	}()
}

func (s *Service) applyDeviceSnapshot(version panel.StateVersion, users map[int][]string) {
	if users == nil || !s.deviceVersion.accept(version) {
		return
	}
	s.kernel.UpdateGlobalDevices(users)
	s.lastDeviceSync = time.Now()
}
