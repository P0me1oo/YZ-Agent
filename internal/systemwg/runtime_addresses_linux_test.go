//go:build linux

package systemwg

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestRelayAddressesSupportsPoolFronts(t *testing.T) {
	for _, address := range []string{"10.253.0.1/32", "10.252.0.1/32", "10.252.255.255/32"} {
		if err := checkAddresses(&model.RelayWireGuardConfig{Address: []string{address}}, true); err != nil {
			t.Fatalf("面板生成的前置地址被拒绝: %s: %v", address, err)
		}
	}
	if err := checkAddresses(&model.RelayWireGuardConfig{Address: []string{"10.253.0.2/32"}}, false); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"10.252.0.0/32", "10.252.1.1/24", "10.254.0.1/32", "0.0.0.0/0", "fd7a:797a:1::1/128"} {
		if err := checkAddresses(&model.RelayWireGuardConfig{Address: []string{address}}, true); err == nil {
			t.Fatalf("接受了非面板规则的前置地址: %s", address)
		}
	}
	if err := checkAddresses(&model.RelayWireGuardConfig{Address: []string{"10.252.0.1/32"}}, false); err == nil {
		t.Fatal("落地地址被前置地址替代")
	}
}

func TestNextRelayRejectsBroadPeerRoutes(t *testing.T) {
	w := &model.RelayWireGuardConfig{Transport: "socks-v1", Address: []string{"10.253.0.1/32", "fd7a:797a::1/128"}, AllowedIPs: []string{"10.253.0.2/32", "fd7a:797a::2/128"}}
	if err := checkAddresses(w, true); err != nil {
		t.Fatal(err)
	}
	w.AllowedIPs = []string{"0.0.0.0/0", "::/0"}
	if err := checkAddresses(w, true); err == nil {
		t.Fatal("Next accepted default routes in isolated SOCKS tunnel")
	}
}
