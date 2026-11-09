package main

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// Startup can fail before the normal handshake starts. Resolve logging policy
// on this error path only; unknown/disabled identities must still remain silent.
func startupFailure(root *config.RootConfig, operation string, cause error) {
	instances, err := root.NormalizeInstances()
	if err != nil {
		return
	}
	configureLogPolicies(instances)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seen := make(map[string]bool)
	for _, instance := range instances {
		if instance.IsStandalone() {
			continue
		}
		nodes := instance.ExpandNodes()
		if instance.IsMachineMode() {
			nodes = []*config.Config{instance}
		}
		for _, node := range nodes {
			cfg := node.Panel
			if node.Machine != nil {
				cfg.MachineID, cfg.Token, cfg.NodeID = node.Machine.MachineID, node.Machine.Token, 0
			}
			key := nlog.LogPolicyKey(cfg.URL, cfg.MachineID, cfg.NodeID)
			if seen[key] {
				continue
			}
			seen[key] = true
			_, _ = panel.NewClient(cfg).HandshakeContext(ctx)
		}
	}
	nlog.Core().Error(operation, "error", nlog.Sanitize(cause.Error()))
}
