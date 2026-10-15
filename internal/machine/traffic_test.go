package machine

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestTrafficProtectionCancelsAllProxiesButPreservesManagement(t *testing.T) {
	management, cancel := context.WithCancel(context.Background())
	defer cancel()
	one, cancelOne := context.WithCancel(management)
	defer cancelOne()
	two, cancelTwo := context.WithCancel(management)
	defer cancelTwo()
	o := &Orchestrator{cfg: &config.Config{}, nodes: map[int]*nodeHandle{1: {cancel: cancelOne}, 2: {cancel: cancelTwo}}}
	for range 2 {
		o.setTrafficBlocked(true)
	}
	if one.Err() == nil || two.Err() == nil {
		t.Fatal("not all managed proxies were cancelled")
	}
	if management.Err() != nil {
		t.Fatal("management connection was cancelled")
	}
	o.startNode(management, panel.MachineNode{ID: 3})
	if len(o.nodes) != 2 {
		t.Fatal("discovery bypassed traffic protection")
	}
	o.setTrafficBlocked(false)
	if len(o.nodes) != 2 {
		t.Fatal("recovery started nodes without an administrator-enabled discovery snapshot")
	}
}
