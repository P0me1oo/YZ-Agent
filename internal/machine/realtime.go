package machine

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/monitor"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func (o *Orchestrator) publishRuntimeState(ctx context.Context) {
	if !o.client.RealtimeEnabled() || !o.stateActive.CompareAndSwap(false, true) {
		return
	}
	var load map[string]interface{}
	if details, _ := o.client.TelemetryNeeds(); details {
		status := monitor.Collect()
		load = map[string]interface{}{
			"cpu":  status.CPU,
			"mem":  map[string]uint64{"total": status.MemTotal, "used": status.MemUsed},
			"swap": map[string]uint64{"total": status.SwapTotal, "used": status.SwapUsed},
			"disk": map[string]uint64{"total": status.DiskTotal, "used": status.DiskUsed},
		}
		if status.NetInSpeed >= 0 && status.NetOutSpeed >= 0 {
			load["net"] = map[string]float64{"in_speed": status.NetInSpeed, "out_speed": status.NetOutSpeed}
		}
	}
	go func() {
		defer o.stateActive.Store(false)
		reportCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		if err := o.client.PublishState(reportCtx, panel.StatePayload{Status: load}); err != nil && ctx.Err() == nil {
			nlog.Core().Warn("machine realtime state report failed", "error", err)
		}
	}()
}
