package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestRelayDDNSOutbounds(t *testing.T) {
	for _, protocol := range []string{"shadowsocks", "vless", "wireguard"} {
		t.Run(protocol, func(t *testing.T) {
			child := model.RelayChild{NodeID: 7, Tag: "relay-7", Protocol: protocol,
				Address: "landing.example.test", ResolvedAddress: "2001:db8::7", Port: 443,
				VLESS: &model.RelayVLESSConfig{Network: "tcp", TLS: 1,
					TLSSettings: map[string]any{"server_name": "tls.example.test"}},
				WireGuard: &model.RelayWireGuardConfig{}}
			node := &model.NodeSpec{Relay: &model.RelayConfig{Mode: "entry", Children: []model.RelayChild{child}}}
			outbounds := buildRelayOutbounds(node)
			if protocol == "wireguard" {
				outbounds = buildRelayWireGuardEndpoints(node)
			}
			raw, err := json.Marshal(outbounds)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if !strings.Contains(text, "2001:db8::7") || strings.Contains(text, "landing.example.test") {
				t.Fatalf("confirmed IP missing from outbound: %s", text)
			}
			if protocol == "vless" && !strings.Contains(text, "tls.example.test") {
				t.Fatal("TLS identity changed with connection address")
			}
		})
	}
}
