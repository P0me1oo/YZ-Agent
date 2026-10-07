package model

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestWireGuardValidationAndPanelRoundTrip(t *testing.T) {
	a, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	w := &RelayWireGuardConfig{PrivateKey: base64.StdEncoding.EncodeToString(a.Bytes()), PeerPublicKey: base64.StdEncoding.EncodeToString(b.PublicKey().Bytes()), Address: []string{"10.253.0.2/32"}, AllowedIPs: []string{"10.253.0.1/32"}, MTU: 1380, Keepalive: 25}
	node := &NodeSpec{Protocol: "wireguard", ServerPort: 51820, Relay: &RelayConfig{Mode: "landing", Protocol: "wireguard", WireGuard: w}}
	for _, kind := range []string{"xray", "singbox"} {
		if err := ValidateNodeSpec(node, config.KernelConfig{Type: kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, mtu := range []int{1280, 1380, 1420, 1500} {
		candidate := cloneRelayWireGuard(w)
		candidate.MTU = mtu
		if err := validateRelayWireGuard(candidate); err != nil {
			t.Fatalf("合法 MTU %d 被拒绝: %v", mtu, err)
		}
	}
	if err := ValidateNodeSpec(&NodeSpec{Protocol: "wireguard"}, config.KernelConfig{Type: "xray"}); err == nil {
		t.Fatal("缺少直连身份或中转配置的 WG 被接受")
	}
	for name, change := range map[string]func(*RelayWireGuardConfig){
		"private": func(w *RelayWireGuardConfig) { w.PrivateKey = "invalid" },
		"peer":    func(w *RelayWireGuardConfig) { w.PeerPublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32)) },
		"self": func(w *RelayWireGuardConfig) {
			w.PeerPublicKey = base64.StdEncoding.EncodeToString(a.PublicKey().Bytes())
		},
		"mtu":       func(w *RelayWireGuardConfig) { w.MTU = 1279 },
		"mtu_upper": func(w *RelayWireGuardConfig) { w.MTU = 1501 },
		"keepalive": func(w *RelayWireGuardConfig) { w.Keepalive = -1 },
		"address":   func(w *RelayWireGuardConfig) { w.Address = []string{"bad"} },
		"allowed":   func(w *RelayWireGuardConfig) { w.AllowedIPs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneRelayWireGuard(w)
			change(bad)
			if validateRelayWireGuard(bad) == nil {
				t.Fatal("无效配置未拒绝")
			}
		})
	}
	copy := cloneRelayWireGuard(w)
	if got := NodeSpecFromPanel(node.ToPanel()); !reflect.DeepEqual(got.Relay, node.Relay) {
		t.Fatal("面板配置往返转换改变 WG 参数")
	}
	if !reflect.DeepEqual(w, copy) {
		t.Fatal("复制改变配置")
	}
	copy.Address[0] = "192.0.2.1/32"
	if w.Address[0] == copy.Address[0] {
		t.Fatal("复制共享了地址切片")
	}
}
