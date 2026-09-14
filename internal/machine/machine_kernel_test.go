package machine

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestMachineNodeKernelDefaultsToXray(t *testing.T) {
	o := &Orchestrator{cfg: &config.Config{Kernel: config.KernelConfig{Type: "xray"}}}

	if got := o.machineNodeKernel(panel.MachineNode{ID: 1}); got != "xray" {
		t.Fatalf("empty node kernel = %q, want xray", got)
	}

	o.cfg.Kernel.Type = ""
	if got := o.machineNodeKernel(panel.MachineNode{ID: 1}); got != "xray" {
		t.Fatalf("empty node and fallback kernel = %q, want xray", got)
	}
}

func TestMachineNodeKernelUsesNodeOverride(t *testing.T) {
	for _, fallback := range []string{"xray", "singbox"} {
		o := &Orchestrator{cfg: &config.Config{Kernel: config.KernelConfig{Type: fallback}}}
		for _, nodeType := range []string{"shadowsocks", "vless"} {
			for _, kernel := range []string{"xray", "singbox"} {
				if got := o.machineNodeKernel(panel.MachineNode{ID: 1, Type: nodeType, KernelType: kernel}); got != kernel {
					t.Fatalf("fallback=%s, protocol=%s: node kernel = %q, want %s", fallback, nodeType, got, kernel)
				}
			}
		}
	}
}
