package service

import (
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/scheduling"
	"testing"
)

func TestProbeTargetsDoNotReloadKernelAndSurvivePanelRoundTrip(t *testing.T) {
	wire := &panel.NodeConfig{Protocol: "vless", SchedulingTargets: []scheduling.Target{{NodeID: 1, Address: "127.0.0.1", Port: 443, Token: "test"}}}
	spec := model.NodeSpecFromPanel(wire)
	baseline := computeConfigHash(spec)
	spec.SchedulingTargets[0].Port = 8443
	if wire.SchedulingTargets[0].Port != 443 {
		t.Fatal("panel conversion shares mutable targets")
	}
	if computeConfigHash(spec) != baseline {
		t.Fatal("probe targets must not change kernel configuration hash")
	}
	restored := spec.ToPanel()
	if restored.SchedulingTargets[0].Port != 8443 {
		t.Fatal("target lost during conversion")
	}
	restored.SchedulingTargets[0].Port = 443
	if spec.SchedulingTargets[0].Port != 8443 {
		t.Fatal("reverse conversion shares mutable targets")
	}
}
