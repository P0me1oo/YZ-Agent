//go:build linux

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type countedRelayKernel struct {
	kernel.Kernel
	reloads int
}

func (k *countedRelayKernel) Reload(node *model.NodeSpec, users []model.UserSpec, cert kernel.TLSCert) error {
	k.reloads++
	return k.Kernel.Reload(node, users, cert)
}

func TestNativeRelayNextTCPProbe(t *testing.T) {
	raw := os.Getenv("YZ_RELAY_TCP_PROBES")
	if raw == "" {
		t.Skip("isolated client only")
	}
	var probes []nativeSourceSOCKSProbe
	if err := json.Unmarshal([]byte(raw), &probes); err != nil {
		t.Fatal(err)
	}
	for _, p := range probes {
		conn, err := net.DialTimeout("tcp", p.Address, 400*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		if (err == nil) != p.Allowed {
			t.Fatalf("source admission %s: allowed=%v err=%v", p.Address, p.Allowed, err)
		}
	}
}

// Consumes exact Next snapshots. Run only in the same isolated environment as
// firewall's native tests; no production identities or network are used.
func TestNativeRelayNextSnapshotsBothCores(t *testing.T) {
	dir := os.Getenv("TEST_RELAY_FIREWALL_EXPORT")
	if dir == "" || os.Getenv("YZ_FIREWALL_NATIVE") != "1" {
		t.Skip("requires exported Next snapshots and isolated networking")
	}
	marker, err := os.ReadFile("/run/yz-firewall-test")
	if err != nil || string(marker) != "isolated-rootfs\n" || os.Geteuid() != 0 {
		t.Fatal("missing isolation marker")
	}
	if _, err := net.InterfaceByName("yzfw0"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"xray", "singbox"} {
		for _, protocol := range []string{"shadowsocks", "vless"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join(dir, protocol+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var input panel.NodeConfig
				if err := json.Unmarshal(raw, &input); err != nil {
					t.Fatal(err)
				}
				node := model.NodeSpecFromPanel(&input)
				if !node.HasRelayFirewall() || node.Relay.Firewall.Status != "ready" || len(node.Relay.Firewall.Sources) != 2 {
					t.Fatal("Next policy did not survive Agent decoding")
				}
				// Test build time must not expire the exported evidence before execution.
				// Only the lifetime is renewed; all sources/ports/config remain as issued.
				node.Relay.Firewall.ValidUntil = time.Now().Add(time.Minute).Unix()
				cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
				core := &countedRelayKernel{}
				if kind == "xray" {
					core.Kernel = xray.New(cfg)
				} else {
					core.Kernel = singbox.New(cfg)
				}
				s := newTestService(&fakeKernel{})
				s.kernel, s.cfg.Kernel, s.lastConfig = core, cfg, node
				manager, err := firewall.New(config.FirewallConfig{Backend: "none", SourceBackend: os.Getenv("YZ_RELAY_DRIVER"), StateDir: t.TempDir()}, filepath.Join(t.TempDir(), "agent.yml"))
				if err != nil {
					t.Fatal(err)
				}
				s.SetFirewallController(manager)
				s.timeConsumer = "next-relay/" + kind
				t.Cleanup(func() {
					s.stopKernel()
					if err := manager.Close(); err != nil {
						t.Error(err)
					}
				})
				probe := func(allowed4, allowed6 bool) {
					t.Helper()
					data, _ := json.Marshal([]nativeSourceSOCKSProbe{
						{Address: fmt.Sprintf("8.8.8.1:%d", node.ServerPort), Allowed: allowed4},
						{Address: fmt.Sprintf("[2001:4860:4860::1]:%d", node.ServerPort), Allowed: allowed6},
					})
					exe, _ := os.Executable()
					cmd := exec.CommandContext(t.Context(), "ip", "netns", "exec", "yzfw-client", exe, "-test.run=^TestNativeRelayNextTCPProbe$")
					cmd.Env = append(os.Environ(), "YZ_RELAY_TCP_PROBES="+string(data))
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("%v: %s", err, out)
					}
				}
				if !s.ensureRunning(t.Context()) || s.firewallNotice.Load() != nil {
					t.Fatalf("protected start: %v / %v", s.runtimeError, s.firewallNotice.Load())
				}
				probe(true, true)
				next := *node
				relay := *node.Relay
				policy := *relay.Firewall
				policy.Sources = []string{"9.9.9.9"}
				relay.Firewall, next.Relay = &policy, &relay
				if !s.applyConfigUpdate(context.Background(), &next, computeConfigHash(&next)) {
					t.Fatal("source update rejected")
				}
				probe(false, false)
				pending := next
				pendingRelay, pendingPolicy := relay, policy
				pendingPolicy.Status = "pending"
				pendingRelay.Firewall, pending.Relay = &pendingPolicy, &pendingRelay
				if !s.applyConfigUpdate(t.Context(), &pending, computeConfigHash(&pending)) || !core.IsRunning() || s.firewallNotice.Load() == nil {
					t.Fatal("pending must retain running core and warn")
				}
				probe(false, false)
				if !s.applyConfigUpdate(t.Context(), node, computeConfigHash(node)) || s.firewallNotice.Load() != nil {
					t.Fatal("policy recovery failed")
				}
				probe(true, true)
				if core.reloads != 0 {
					t.Fatal("source changes restarted core")
				}
			})
		}
	}
}
