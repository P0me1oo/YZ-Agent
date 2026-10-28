package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestNodeSpecFromPanelValidated_EncryptedCustomConfig(t *testing.T) {
	for _, kernel := range []string{"xray", "singbox"} {
		for _, format := range []struct{ name, data string }{
			{"json", `{"outbounds":[{"tag":"custom-exit"}]}`},
			{"yaml", "outbounds:\n  - tag: custom-exit\n"},
		} {
			t.Run(kernel+"/"+format.name, func(t *testing.T) {
				dir := t.TempDir()
				state := privacytest.Bind(t, dir)
				path := filepath.Join(dir, "custom."+format.name)
				if err := privacy.WriteFile(path, []byte(format.data), 0o600); err != nil {
					t.Fatal(err)
				}
				stored, err := os.ReadFile(path)
				if err != nil || !privacy.IsEncrypted(stored) {
					t.Fatalf("custom config must be encrypted: %v", err)
				}
				node := &panel.NodeConfig{Protocol: "shadowsocks", ServerPort: 8388,
					CustomRouteRules: []panel.CustomRouteRule{{
						Action: panel.RouteAction{Type: "route", Target: "custom-exit"},
						Match:  panel.RouteMatch{DomainSuffixes: []string{"example.com"}},
					}},
				}
				kcfg := config.KernelConfig{Type: kernel, CustomConfig: path}
				for attempt := 0; attempt < 2; attempt++ {
					if _, err := NodeSpecFromPanelValidated(node, kcfg); err != nil {
						t.Fatalf("startup/reload validation failed: %v", err)
					}
				}
				duplicate := kcfg
				duplicate.CustomOutbound = []map[string]any{{"tag": "custom-exit"}}
				if _, err := NodeSpecFromPanelValidated(node, duplicate); err == nil || !strings.Contains(err.Error(), "duplicates") {
					t.Fatalf("decrypted outbound collisions must still be rejected: %v", err)
				}
				privacy.Bind(dir, nil, nil, true)
				if _, err := NodeSpecFromPanelValidated(node, kcfg); err == nil || !strings.Contains(err.Error(), "read custom config") {
					t.Fatalf("unbound encrypted config must fail at reading: %v", err)
				}
				privacy.Bind(dir, state, nil, true)
				if _, err := NodeSpecFromPanelValidated(node, kcfg); err != nil {
					t.Fatalf("restored binding must allow validation: %v", err)
				}
			})
		}
	}
}

func TestNodeSpecFromPanelValidated_AllowsTargetsFromCustomConfigOutbounds(t *testing.T) {
	dir := t.TempDir()
	customConfigPath := filepath.Join(dir, "custom.json")
	if err := os.WriteFile(customConfigPath, []byte(`{"outbounds":[{"tag":"wg-exit","protocol":"wireguard"}]}`), 0o644); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 8388,
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "proxy",
			Protocol: "socks",
			ProxyTag: "wg-exit",
			Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080},
		}},
		CustomRouteRules: []panel.CustomRouteRule{{
			Action: panel.RouteAction{Type: "route", Target: "wg-exit"},
			Match:  panel.RouteMatch{DomainSuffixes: []string{"example.com"}},
		}},
	}, config.KernelConfig{Type: "singbox", CustomConfig: customConfigPath})
	if err != nil {
		t.Fatalf("expected custom_config outbound tags to be available, got %v", err)
	}
}

func TestNodeSpecFromPanelValidated_RejectsDuplicateTagsAcrossSources(t *testing.T) {
	dir := t.TempDir()
	customConfigPath := filepath.Join(dir, "custom.yaml")
	if err := os.WriteFile(customConfigPath, []byte("outbounds:\n  - tag: warp\n    protocol: wireguard\n"), 0o644); err != nil {
		t.Fatalf("write custom config: %v", err)
	}

	_, err := NodeSpecFromPanelValidated(&panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 8388,
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "warp",
			Protocol: "socks",
			Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080},
		}},
	}, config.KernelConfig{
		Type:         "singbox",
		CustomConfig: customConfigPath,
		CustomOutbound: []map[string]any{{
			"tag":      "proxy",
			"protocol": "socks",
		}},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `custom_outbounds[0].tag duplicates "warp" from kernel.custom_config.outbounds[0]`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOutboundTagCollisions_RejectsDuplicateRawSources(t *testing.T) {
	err := validateOutboundTagCollisions(nil, []outboundTagSource{
		{Tag: "warp", Source: "kernel.custom_outbound[0]"},
		{Tag: "warp", Source: "kernel.custom_config.outbounds[0]"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `kernel.custom_config.outbounds[0].tag duplicates "warp" from kernel.custom_outbound[0]`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
