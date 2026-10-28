package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xrayKernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
	"gopkg.in/yaml.v3"
)

func TestEncryptedCustomConfigStartsAndReloadsBothCores(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, format := range []string{"json", "yaml"} {
			t.Run(kind+"/"+format, func(t *testing.T) {
				dir := t.TempDir()
				state := privacytest.Bind(t, dir)
				path := filepath.Join(dir, "config.yml")
				outbound := map[string]any{"tag": "custom-exit", "type": "direct"}
				if kind == "xray" {
					outbound = map[string]any{"tag": "custom-exit", "protocol": "freedom"}
				}
				custom := map[string]any{"outbounds": []map[string]any{outbound}}
				marshal := json.Marshal
				if format == "yaml" {
					marshal = yaml.Marshal
				}
				data, err := marshal(custom)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := privacy.MigrateFile(path, state); err != nil {
					t.Fatal(err)
				}
				stored, err := os.ReadFile(path)
				if err != nil || !privacy.IsEncrypted(stored) {
					t.Fatalf("managed config.yml must be encrypted: %v", err)
				}
				cfg := config.KernelConfig{Type: kind, ConfigDir: dir, CustomConfig: path, LogLevel: "error"}
				var core kernel.Kernel
				if kind == "xray" {
					core = xrayKernel.New(cfg)
				} else {
					core = New(cfg)
				}
				t.Cleanup(core.Stop)
				node := runtimeNode(t, "socks")
				node.CustomRoutes = nil
				node.CustomRouteRules = []model.CustomRouteRule{{
					Match:  model.RouteMatch{IPCIDRs: []string{"127.0.0.1/32"}},
					Action: model.RouteAction{Type: "route", Target: "custom-exit"},
				}}
				users := []model.UserSpec{runtimeUser(t, 981)}
				echo := runtimeEcho(t)
				for attempt := 0; attempt < 3; attempt++ {
					if err := model.ValidateNodeSpec(node, cfg); err != nil {
						t.Fatalf("startup/reload validation failed: %v", err)
					}
					apply := core.Reload
					if attempt == 0 {
						apply = core.Start
					}
					if err := apply(node, users, kernel.TLSCert{}); err != nil {
						t.Fatalf("startup/reload failed: %v", err)
					}
					conn := runtimeDial(t, runtimeClient(t, node, users[0]), echo)
					runtimeExchange(t, conn)
					_ = conn.Close()
				}
			})
		}
	}
}
