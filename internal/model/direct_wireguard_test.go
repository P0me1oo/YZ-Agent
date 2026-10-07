package model

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

func TestDirectWireGuardRoundTripAndSourcePolicy(t *testing.T) {
	key, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	input := &panel.NodeConfig{Protocol: "wireguard", KernelType: "singbox", WireGuard: &panel.WireGuardConfig{PrivateKey: base64.StdEncoding.EncodeToString(key.Bytes()), Address: []string{"10.0.0.1/32"}, MTU: 1420}, SourcePolicy: &sourcepolicy.Policy{BlockCN: true}}
	spec, e := NodeSpecFromPanelValidated(input, config.KernelConfig{Type: "singbox"})
	if e != nil {
		t.Fatal(e)
	}
	output := spec.ToPanel()
	output.WireGuard.Address[0] = "10.0.0.9/32"
	if input.WireGuard.Address[0] != spec.WireGuard.Address[0] || spec.WireGuard.Address[0] == output.WireGuard.Address[0] {
		t.Fatal("节点隧道地址被共享修改")
	}
	users := []panel.User{{ID: 1, WireGuard: &panel.WireGuardPeer{PublicKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), Address: []string{"10.0.0.2/32"}, ExpiresAt: 2000000000}}}
	models := UserSpecsFromPanel(users)
	roundtrip := UserSpecsToPanel(models)
	roundtrip[0].WireGuard.Address[0] = "10.0.0.8/32"
	if models[0].WireGuard.ExpiresAt != users[0].WireGuard.ExpiresAt || models[0].WireGuard.Address[0] != users[0].WireGuard.Address[0] {
		t.Fatal("用户身份往返不一致")
	}
	for _, address := range []string{"0.0.0.0/0", "10.0.0.0/24", "8.8.8.8/32"} {
		spec.WireGuard.Address = []string{address}
		if ValidateDirectWireGuard(spec) == nil {
			t.Fatal("非法隧道地址被接受")
		}
	}
}
