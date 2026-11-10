package main

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func runtimeProfile(root *config.RootConfig, instances []*config.Config) *config.Config {
	if len(instances) > 0 {
		return instances[0]
	}
	return &root.Config
}

func watchDeletedBindings(ctx context.Context, path string, reload func(*config.RootConfig)) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var failures nlog.Failures
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Each endpoint has its own five-second limit. Do not share the startup
		// budget here: earlier unreachable panels must not starve later bindings.
		changed, err := config.PruneDeletedMachines(ctx, path)
		failures.Report(nlog.Core(), "绑定清理未完成，稍后重试", err)
		if changed {
			if root, err := config.LoadRoot(path); err == nil {
				reload(root)
			}
		}
	}
}
