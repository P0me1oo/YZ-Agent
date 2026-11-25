package model

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestRelayDDNSRoundTripAndValidation(t *testing.T) {
	for _, address := range []string{"", "192.0.2.8", "2001:db8::8", "not-an-ip", "0.0.0.0", "ff02::1"} {
		t.Run(address, func(t *testing.T) {
			panelConfig := entrySpec()
			panelConfig.Relay.Children[0].Address = "landing.example.test"
			panelConfig.Relay.Children[0].ResolvedAddress = address
			node, err := NodeSpecFromPanelValidated(panelConfig, config.KernelConfig{Type: "xray"})
			invalid := address == "not-an-ip" || address == "0.0.0.0" || address == "ff02::1"
			if invalid {
				if err == nil {
					t.Fatal("invalid confirmed IP accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			child := node.Relay.Children[0]
			want := address
			if want == "" {
				want = "landing.example.test"
			}
			if child.ConnectAddress() != want || child.Address != "landing.example.test" {
				t.Fatal("domain and confirmed address were mixed")
			}
			if node.ToPanel().Relay.Children[0].ResolvedAddress != address {
				t.Fatal("confirmed address lost on round trip")
			}
		})
	}
}
