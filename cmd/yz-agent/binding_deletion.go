package main

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func runtimeProfile(root *config.RootConfig, instances []*config.Config) *config.Config {
	if len(instances) > 0 {
		return instances[0]
	}
	return &root.Config
}

// Only retry while a local cleanup journal is incomplete. No panel request is needed.
func resumeBindingCleanup(ctx context.Context, path string, changed <-chan struct{}) {
	for ctx.Err() == nil {
		var retry <-chan time.Time
		if err := config.FinishPendingBindingCleanup(ctx, path); err != nil {
			retry = time.After(10 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-retry:
		}
	}
}
