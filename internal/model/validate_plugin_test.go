package model

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestValidateShadowsocksPlugin(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, kernel, plugin, network, options string
		wantError                                        bool
	}{
		{name: "supported", protocol: "shadowsocks", kernel: "xray", plugin: "v2ray-plugin", network: "ws", options: "mode=websocket;mux=0"},
		{name: "no plugin", protocol: "shadowsocks", kernel: "singbox", network: "tcp"},
		{name: "singbox plugin", protocol: "shadowsocks", kernel: "singbox", plugin: "v2ray-plugin", network: "ws", options: "mode=websocket;mux=0", wantError: true},
		{name: "other protocol", protocol: "vless", kernel: "xray", plugin: "v2ray-plugin", network: "ws", options: "mode=websocket;mux=0", wantError: true},
		{name: "other plugin", protocol: "shadowsocks", kernel: "xray", plugin: "obfs-local", network: "ws", options: "mode=websocket;mux=0", wantError: true},
		{name: "wrong transport", protocol: "shadowsocks", kernel: "xray", plugin: "v2ray-plugin", network: "tcp", options: "mode=websocket;mux=0", wantError: true},
		{name: "implicit mux", protocol: "shadowsocks", kernel: "xray", plugin: "v2ray-plugin", network: "ws", options: "mode=websocket", wantError: true},
		{name: "enabled mux", protocol: "shadowsocks", kernel: "xray", plugin: "v2ray-plugin", network: "ws", options: "mode=websocket;mux=1", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &NodeSpec{Protocol: tc.protocol, Plugin: tc.plugin, PluginOpt: tc.options, Network: tc.network}
			err := ValidateNodeSpec(node, config.KernelConfig{Type: tc.kernel})
			if (err != nil) != tc.wantError {
				t.Fatalf("插件组合校验：error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}
