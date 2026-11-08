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

func (s *Service) collectStatus() monitor.Status {
	enabled := true
	if policy, ok := s.source.(interface{ HostMonitoringEnabled() bool }); ok {
		enabled = policy.HostMonitoringEnabled()
	}
	return monitor.CollectWithHost(enabled)
}

// publishRuntimeState 不阻塞主循环。慢通道期间不堆积采样，下一次发送直接取最新快照。
func (s *Service) publishRuntimeState(ctx context.Context) {
	publisher, ok := s.sink.(controlplane.StatePublisher)
	if !ok || !publisher.RealtimeEnabled() || !s.stateActive.CompareAndSwap(false, true) {
		return
	}
	details, speeds := true, true
	if demand, ok := publisher.(interface{ TelemetryNeeds() (bool, bool) }); ok {
		details, speeds = demand.TelemetryNeeds()
	}
	counts, relayCounts := s.tracker.CurrentConnectionCounts()
	state := panel.StatePayload{
		Alive: s.tracker.FlushAliveIPs(), Online: s.tracker.CurrentOnline(),
		ConnectionCounts: counts, RelayConnectionCounts: relayCounts,
		IPConnectionCounts: s.tracker.CurrentIPConnectionCounts(),
		RelayUserAlive:     s.tracker.RelayUserAlive(),
	}
	if speeds {
		state.UserSpeeds = s.userSpeed.rates
	}
	if value := s.operations.snapshot.Load(); value != nil {
		state.Operations = value
	}
	if details {
		status := s.collectStatus()
		state.Metrics = s.buildMetrics(status)
		// 实时通道已在顶层发送运行状态；指标中的副本只供旧报告路径使用。
		delete(state.Metrics, "operations")
		state.Metrics["kernel_status"] = s.kernel.IsRunning()
		state.Status = map[string]interface{}{
			"cpu":           status.CPU,
			"mem":           map[string]uint64{"total": status.MemTotal, "used": status.MemUsed},
			"swap":          map[string]uint64{"total": status.SwapTotal, "used": status.SwapUsed},
			"disk":          map[string]uint64{"total": status.DiskTotal, "used": status.DiskUsed},
			"kernel_status": s.kernel.IsRunning(),
		}
		if status.HostSkipped {
			state.Status = map[string]interface{}{"kernel_status": s.kernel.IsRunning()}
		}
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

// 稳定名单由面板重复发送以续期；只有超过三十五秒没有有效快照才清除。
func (s *Service) expireDeviceSnapshot(now time.Time) {
	if !s.lastDeviceSync.IsZero() && now.Sub(s.lastDeviceSync) > 35*time.Second {
		s.kernel.ClearGlobalDevices()
		s.lastDeviceSync = time.Time{}
	}
}
