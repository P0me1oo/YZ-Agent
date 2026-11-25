//go:build with_quic

package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestRelayDDNSRuntimeSwitch(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			user := runtimeUser(t, 1881)
			landing := runtimeNode(t, "shadowsocks")
			landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks",
				Cipher: "aes-128-gcm", Password: runtimeUser(t, 1882).UUID}
			relayStartCore(t, kind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "old"))
			nextLanding := *landing
			nextLanding.ListenIP = "127.0.0.2"
			relayStartCore(t, kind, &nextLanding, nil, kernel.TLSCert{}, relayMarkerEcho(t, "new"))
			node := runtimeNode(t, "vless")
			node.Relay = &model.RelayConfig{Mode: "entry", Accounting: "routes", RouteID: 11,
				Children: []model.RelayChild{{NodeID: 7, Tag: "relay-7", RouteID: 12,
					Protocol: "shadowsocks", Address: "unresolvable.invalid", ResolvedAddress: "127.0.0.1",
					Port: landing.ServerPort, Cipher: landing.Relay.Cipher, Password: landing.Relay.Password}}}
			entry := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
			clientUser := user
			clientUser.UUID = relayCredential(user, 12)
			relayMarkerExchange(t, runtimeClient(t, node, clientUser), "old", "tcp")
			next := model.NodeSpecFromPanel(node.ToPanel())
			next.Relay.Children[0].ResolvedAddress = "127.0.0.2"
			for range 2 {
				if err := entry.Reload(next, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				relayMarkerExchange(t, runtimeClient(t, next, clientUser), "new", "tcp")
			}
		})
	}
}
