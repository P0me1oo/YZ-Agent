package machine

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/portforward"
)

// A single loop owns the manager and its children. Startup requires online authorization.
func (o *Orchestrator) forwardingLoop(ctx context.Context, m *portforward.Manager) {
	defer func() {
		if err := m.Close(); err != nil {
			o.logger().Error("forwarding cleanup failed", "error", err)
		}
	}()
	// Stale desired files never authorize a new process. Clean owned residual
	// NAT before waiting for the first authenticated policy from the panel.
	policy := portforward.Policy{Version: 1}
	statuses := m.Apply(ctx, policy)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		next := 5 * time.Second
		requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		desired, err := o.client.ExchangeForwarding(requestCtx, statuses)
		cancel()
		if err == nil {
			policy = desired
		} else if errors.Is(err, panel.ErrForwardingUnauthorized) {
			policy = portforward.Policy{Version: 1}
		} else if errors.Is(err, panel.ErrForwardingUnsupported) {
			next = time.Minute
		}
		applyCtx, applyCancel := context.WithTimeout(ctx, 15*time.Second)
		statuses = m.Apply(applyCtx, policy)
		applyCancel()
		// Revocations take effect before disk I/O. A failed save cannot restore
		// previous access, even after restart, because startup never applies cache.
		if err == nil || errors.Is(err, panel.ErrForwardingUnauthorized) {
			if saveErr := m.Save(policy); saveErr != nil {
				o.logger().Error("cannot persist forwarding policy", "error", saveErr)
				if len(statuses) == 0 {
					statuses = append(statuses, portforward.Status{ID: 0, State: "stopped"})
				}
				for i := range statuses {
					statuses[i].Warning = strings.TrimSpace(statuses[i].Warning + " 转发策略未能保存；重启后等待面板重新授权")
				}
			}
		}
		timer.Reset(next)
	}
}
func (o *Orchestrator) newForwardingManager() (*portforward.Manager, error) {
	targets := make([]portforward.TargetPermission, 0, len(o.cfg.Forwarding.AllowedTargets))
	for _, target := range o.cfg.Forwarding.AllowedTargets {
		prefix, err := netip.ParsePrefix(target.CIDR)
		if err != nil {
			return nil, err
		}
		targets = append(targets, portforward.TargetPermission{Prefix: prefix, Ports: target.Ports})
	}
	return portforward.New(o.cfg.Kernel.ConfigDir, fmt.Sprintf("%s|%d", o.cfg.Panel.URL, o.cfg.Machine.MachineID), targets...)
}
