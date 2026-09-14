package model

import (
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func entrySpec() *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:   "vless",
		ServerPort: 24443,
		Relay: &panel.RelayConfig{
			Mode:    panel.RelayModeEntry,
			RouteID: 11,
			Children: []panel.RelayChild{{
				NodeID:   7,
				Tag:      "relay-7",
				RouteID:  12,
				Protocol: "shadowsocks",
				Address:  "203.0.113.7",
				Port:     28388,
				Cipher:   "2022-blake3-aes-128-gcm",
				Password: "MTIzNDU2Nzg5MGFiY2RlZg==",
			}},
		},
	}
}

func landingSpec() *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 28388,
		Cipher:     "2022-blake3-aes-128-gcm",
		Relay: &panel.RelayConfig{
			Mode:       panel.RelayModeLanding,
			Protocol:   "shadowsocks",
			ListenPort: 28388,
			Cipher:     "2022-blake3-aes-128-gcm",
			Password:   "MTIzNDU2Nzg5MGFiY2RlZg==",
		},
	}
}

func vlessLandingSpec(network, decryption string) *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:   "vless",
		ServerPort: 29388,
		Network:    network,
		Decryption: decryption,
		Relay: &panel.RelayConfig{
			Mode:       panel.RelayModeLanding,
			Protocol:   "vless",
			ListenPort: 29388,
			VLESS: &panel.RelayVLESSConfig{
				ID: "11111111-2222-0000-8444-555555555555",
			},
		},
	}
}

func vlessRelayChild(network string, tlsMode int) panel.RelayChild {
	return panel.RelayChild{
		NodeID:   17,
		Tag:      "relay-17",
		RouteID:  17,
		Protocol: "vless",
		Address:  "10.0.0.17",
		Port:     29388,
		VLESS: &panel.RelayVLESSConfig{
			ID:         "11111111-2222-0000-8444-555555555555",
			Network:    network,
			TLS:        tlsMode,
			Encryption: "none",
			TLSSettings: map[string]interface{}{
				"server_name": "landing.example.com",
			},
			RealitySettings: map[string]interface{}{
				"server_name": "landing.example.com",
				"public_key":  "TESTonlyPUBLICkeyNOTaREALsecret0123456789ab",
				"fingerprint": "chrome",
			},
			TransportAuth: "relay-hysteria-test-auth",
		},
	}
}

func xrayKernel() config.KernelConfig { return config.KernelConfig{Type: "xray"} }

func TestValidateNodeSpec_RelayAccepted(t *testing.T) {
	wsEntry := entrySpec()
	wsEntry.Network = "ws"
	wsEntry.TLS = 1
	wsEntry.Flow = "none"
	for name, nc := range map[string]*panel.NodeConfig{
		"entry":                     entrySpec(),
		"entry ws tls":              wsEntry,
		"landing":                   landingSpec(),
		"vless landing default tcp": vlessLandingSpec("", "none"),
	} {
		t.Run(name, func(t *testing.T) {
			spec, err := NodeSpecFromPanelValidated(nc, xrayKernel())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if name == "entry ws tls" && spec.Flow != "" {
				t.Fatalf("flow none was not normalized: %q", spec.Flow)
			}
		})
	}
}

func TestValidateNodeSpec_RelayVLESSMatrix(t *testing.T) {
	valid := []struct {
		network string
		tls     int
	}{
		{"tcp", 0}, {"tcp", 1}, {"tcp", 2},
		{"ws", 0}, {"ws", 1},
		{"grpc", 0}, {"grpc", 1}, {"grpc", 2},
		{"xhttp", 0}, {"xhttp", 1}, {"xhttp", 2},
		{"httpupgrade", 0}, {"httpupgrade", 1},
		{"kcp", 0}, {"kcp", 1},
		{"hysteria", 1},
	}
	for _, item := range valid {
		nc := entrySpec()
		nc.Relay.Children = []panel.RelayChild{vlessRelayChild(item.network, item.tls)}
		if _, err := NodeSpecFromPanelValidated(nc, xrayKernel()); err != nil {
			t.Fatalf("valid combination %s tls=%d rejected: %v", item.network, item.tls, err)
		}
	}

	validEncryption := entrySpec()
	validEncryption.Relay.Children = []panel.RelayChild{vlessRelayChild("tcp", 0)}
	validEncryption.Relay.Children[0].VLESS.Encryption = "mlkem768x25519plus.native.0rtt." + strings.Repeat("A", 43)
	if _, err := NodeSpecFromPanelValidated(validEncryption, xrayKernel()); err != nil {
		t.Fatalf("valid vless encryption rejected: %v", err)
	}

	invalid := []struct {
		network string
		tls     int
	}{
		{"h2", 1}, {"ws", 2}, {"httpupgrade", 2}, {"kcp", 2},
		{"hysteria", 0}, {"hysteria", 2},
	}
	for _, item := range invalid {
		nc := entrySpec()
		nc.Relay.Children = []panel.RelayChild{vlessRelayChild(item.network, item.tls)}
		if _, err := NodeSpecFromPanelValidated(nc, xrayKernel()); err == nil {
			t.Fatalf("invalid combination %s tls=%d was accepted", item.network, item.tls)
		}
	}

	invalidEncryption := entrySpec()
	invalidEncryption.Relay.Children = []panel.RelayChild{vlessRelayChild("tcp", 0)}
	invalidEncryption.Relay.Children[0].VLESS.Encryption = "mlkem768x25519plus.native.0rtt." + strings.Repeat("A", 42)
	if _, err := NodeSpecFromPanelValidated(invalidEncryption, xrayKernel()); err == nil {
		t.Fatalf("invalid vless encryption was accepted")
	}

	invalidDecryption := vlessLandingSpec("tcp", "mlkem768x25519plus.native.600s."+strings.Repeat("A", 42))
	if _, err := NodeSpecFromPanelValidated(invalidDecryption, xrayKernel()); err == nil {
		t.Fatalf("invalid vless decryption was accepted")
	}
}

func TestValidateNodeSpec_Hysteria2Relay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*panel.NodeConfig)
		kernel string
		want   string
	}{
		{"HY2", func(*panel.NodeConfig) {}, "xray", ""},
		{"HY1", func(n *panel.NodeConfig) { n.Version = 1 }, "xray", "version 2"},
		{"sing-box", func(*panel.NodeConfig) {}, "singbox", ""},
		{"未知混淆", func(n *panel.NodeConfig) { n.Obfs = "unknown" }, "xray", "obfuscation"},
		{"缺少混淆密码", func(n *panel.NodeConfig) { n.Obfs = "salamander" }, "xray", "password"},
		{"ECH缺少密钥", func(n *panel.NodeConfig) {
			n.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true}}
		}, "xray", "ECH"},
		{"ECH内联密钥", func(n *panel.NodeConfig) {
			n.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true, "key": "test-only-pem-placeholder"}}
		}, "xray", ""},
		{"ECH密钥文件", func(n *panel.NodeConfig) {
			n.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true, "key_path": "test-only-ech.pem"}}
		}, "singbox", ""},
		{"ECH空白密钥", func(n *panel.NodeConfig) {
			n.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true, "key": " \n", "key_path": " "}}
		}, "singbox", "ECH"},
		{"混淆", func(n *panel.NodeConfig) {
			n.Obfs, n.ObfsPassword = "salamander", strings.Repeat("test", 4)
		}, "xray", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nc := entrySpec()
			nc.Protocol, nc.Version = "hysteria", 2
			nc.Relay.Children = append(nc.Relay.Children, vlessRelayChild("tcp", 0))
			tc.mutate(nc)
			_, err := NodeSpecFromPanelValidated(nc, config.KernelConfig{Type: tc.kernel})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("合法 HY2 中转配置被拒绝: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("校验错误 = %v，应包含 %q", err, tc.want)
			}
		})
	}
}

func TestValidateNodeSpec_RelayRejected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*panel.NodeConfig)
		kernel config.KernelConfig
		want   string
	}{
		{
			name:   "unsupported singbox transport",
			mutate: func(n *panel.NodeConfig) { n.Network = "mkcp" },
			kernel: config.KernelConfig{Type: "singbox"},
			want:   "does not support vless transport",
		},
		{
			name:   "unsupported entry protocol",
			mutate: func(nc *panel.NodeConfig) { nc.Protocol = "trojan" },
			want:   "requires a vless or hysteria2 inbound",
		},
		{
			name: "entry reality over websocket",
			mutate: func(nc *panel.NodeConfig) {
				nc.Network = "ws"
				nc.TLS = 2
			},
			want: "reality only supports",
		},
		{
			name: "entry h2 removed",
			mutate: func(nc *panel.NodeConfig) {
				nc.Network = "h2"
				nc.TLS = 1
			},
			want: "unsupported vless transport",
		},
		{
			name:   "route id zero",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.RouteID = 0 },
			want:   "must be within 1-65535",
		},
		{
			name:   "route id above range",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].RouteID = 70000 },
			want:   "must be within 1-65535",
		},
		{
			name: "duplicate route id",
			mutate: func(nc *panel.NodeConfig) {
				nc.Relay.Children[0].RouteID = nc.Relay.RouteID
			},
			want: "duplicate route_id",
		},
		{
			name: "duplicate tag",
			mutate: func(nc *panel.NodeConfig) {
				extra := nc.Relay.Children[0]
				extra.NodeID = 8
				extra.RouteID = 13
				nc.Relay.Children = append(nc.Relay.Children, extra)
			},
			want: "duplicate tag",
		},
		{
			name:   "reserved tag",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].Tag = "direct" },
			want:   "reserved",
		},
		{
			name:   "unsupported transit protocol",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].Protocol = "trojan" },
			want:   "unsupported transit protocol",
		},
		{
			name:   "unsupported cipher",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].Cipher = "rc4-md5" },
			want:   "unsupported cipher",
		},
		{
			name:   "empty password",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].Password = "" },
			want:   "password must not be empty",
		},
		{
			name:   "invalid child port",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Children[0].Port = 0 },
			want:   "invalid port",
		},
		{
			name:   "unknown mode",
			mutate: func(nc *panel.NodeConfig) { nc.Relay.Mode = "chain" },
			want:   "unsupported relay mode",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc := entrySpec()
			tc.mutate(nc)
			kcfg := tc.kernel
			if kcfg.Type == "" {
				kcfg = xrayKernel()
			}
			_, err := NodeSpecFromPanelValidated(nc, kcfg)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// The internal outbound tag must not silently shadow an admin-defined outbound.
func TestValidateNodeSpec_RelayTagCollidesWithCustomOutbound(t *testing.T) {
	nc := entrySpec()
	nc.CustomOutbounds = []panel.OutboundConfig{{
		Tag:      "relay-7",
		Protocol: "socks",
		Settings: map[string]any{"servers": []any{}},
	}}

	_, err := NodeSpecFromPanelValidated(nc, xrayKernel())
	if err == nil || !strings.Contains(err.Error(), "collides with a custom outbound") {
		t.Fatalf("error = %v, want a tag collision error", err)
	}
}

func TestValidateNodeSpec_NoRelayUnaffected(t *testing.T) {
	nc := entrySpec()
	nc.Relay = nil
	if _, err := NodeSpecFromPanelValidated(nc, xrayKernel()); err != nil {
		t.Fatalf("plain node rejected: %v", err)
	}
}

func TestRelayCipherHelpers(t *testing.T) {
	if !IsRelayTransitCipher("2022-blake3-aes-128-gcm") || !IsRelayTransitCipher("AES-128-GCM") {
		t.Fatalf("supported ciphers rejected")
	}
	if IsRelayTransitCipher("none") || IsRelayTransitCipher("") {
		t.Fatalf("unsupported ciphers accepted")
	}
	if !IsRelaySS2022Cipher("2022-blake3-chacha20-poly1305") || IsRelaySS2022Cipher("aes-256-gcm") {
		t.Fatalf("ss2022 detection wrong")
	}
}
