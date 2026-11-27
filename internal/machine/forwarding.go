package machine

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/portforward"
)

// A single loop owns the manager and its children. Startup requires online authorization.
func (o *Orchestrator) forwardingLoop(ctx context.Context, m *portforward.Manager, startup <-chan struct{}) {
	defer func() {
		if err := m.Close(); err != nil {
			o.logger().Error("forwarding cleanup failed", "error", err)
		}
	}()
	// Stale desired files never authorize a new process. Clean owned residual
	// NAT before waiting for the first authenticated policy from the panel.
	policy := portforward.Policy{Version: 1}
	statuses := m.Apply(ctx, policy)
	if startup != nil {
		select {
		case <-startup:
		case <-ctx.Done():
			return
		}
	}
	var requests sync.WaitGroup
	defer requests.Wait()
	timer := time.NewTimer(0)
	defer timer.Stop()
	type reply struct {
		policy     portforward.Policy
		err        error
		generation uint64
	}
	replies := make(chan reply, 1)
	var requesting bool
	for {
		persist := false
		select {
		case <-ctx.Done():
			return
		case <-o.forwardingWake:
			// Revoke the previous grant even if the quota was raised again before this wakeup.
			policy = portforward.Policy{Version: 1}
			persist = true
			if !requesting {
				timer.Reset(0)
			}
		case <-timer.C:
			generation := o.trafficGeneration.Load()
			report := slices.Clone(statuses)
			requesting = true
			requests.Go(func() {
				requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
				defer cancel()
				desired, err := o.client.ExchangeForwarding(requestCtx, report)
				replies <- reply{policy: desired, err: err, generation: generation}
			})
			continue
		case r := <-replies:
			requesting = false
			next := 5 * time.Second
			switch {
			case r.generation != o.trafficGeneration.Load() || o.trafficBlocked.Load():
				policy = portforward.Policy{Version: 1}
				next = time.Second
			case r.err == nil:
				policy = r.policy
				persist = true
			case errors.Is(r.err, panel.ErrForwardingUnauthorized):
				policy = portforward.Policy{Version: 1}
				persist = true
			case errors.Is(r.err, panel.ErrForwardingUnsupported):
				next = time.Minute
			}
			timer.Reset(next)
		}
		applyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		statuses = m.Apply(applyCtx, policy)
		cancel()
		if persist {
			if err := m.Save(policy); err != nil {
				o.logger().Error("cannot persist forwarding policy", "error", err)
				if len(statuses) == 0 {
					statuses = append(statuses, portforward.Status{ID: 0, State: "stopped"})
				}
				for i := range statuses {
					statuses[i].Warning = strings.TrimSpace(statuses[i].Warning + " 转发策略未能保存；重启后等待面板重新授权")
				}
			}
		}
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
