package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// 面板允许用 xray 的原生名，sing-box 侧要翻译回 direct/block。
func TestCustomOutbound_DirectAndBlockAliases(t *testing.T) {
	cases := []struct {
		given string
		want  string
	}{
		{"direct", "direct"},
		{"freedom", "direct"},
		{"block", "block"},
		{"blackhole", "block"},
	}

	for _, tc := range cases {
		t.Run(tc.given, func(t *testing.T) {
			nc := &panel.NodeConfig{
				Protocol:   "shadowsocks",
				ServerPort: 1234,
				Cipher:     "aes-128-gcm",
				CustomOutbounds: []panel.OutboundConfig{{
					Tag:      "custom-out",
					Protocol: tc.given,
				}},
			}
			cfg := mustBuildConfig(t, config.KernelConfig{LogLevel: "warn"}, testNodeSpec(nc), testUsers, kernel.TLSCert{})

			obs, _ := cfg["outbounds"].([]M)
			var found M
			for _, ob := range obs {
				if ob["tag"] == "custom-out" {
					found = ob
					break
				}
			}
			if found == nil {
				t.Fatal("outbound custom-out missing")
			}
			if found["type"] != tc.want {
				t.Fatalf("type = %v, want %v", found["type"], tc.want)
			}
		})
	}
}

// sing-box 用 inet4/inet6_bind_address 绑定源地址，settings 应当原样透传。
func TestCustomOutbound_BindAddressPassthrough(t *testing.T) {
	nc := &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 1234,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "direct",
			Protocol: "direct",
			Settings: map[string]any{
				"inet6_bind_address": "2001:db8::1",
				"domain_strategy":    "prefer_ipv6",
			},
		}},
	}
	cfg := mustBuildConfig(t, config.KernelConfig{LogLevel: "warn"}, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	obs, _ := cfg["outbounds"].([]M)
	for _, ob := range obs {
		if ob["tag"] != "direct" {
			continue
		}
		if ob["type"] != "direct" {
			t.Fatalf("type = %v, want direct", ob["type"])
		}
		if ob["inet6_bind_address"] != "2001:db8::1" || ob["domain_strategy"] != "prefer_ipv6" {
			t.Fatalf("绑定参数没有透传: %#v", ob)
		}
		return
	}
	t.Fatal("outbound direct missing")
}
