//go:build linux && with_quic && with_wireguard

package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xrayKernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	singM "github.com/sagernet/sing/common/metadata"
)

// Actual Next snapshots supply the identities, addresses, permissions and WG
// settings. Only local listeners, test DNS and the client protocol vary.
func TestNextWireGuardSnapshotsRealChain(t *testing.T) {
	dir := os.Getenv("TEST_WG_EXPORT")
	if dir == "" {
		t.Skip("requires Next WG export")
	}
	read := func(name string, target any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	for _, front := range []string{"xray", "singbox"} {
		for _, back := range []string{"xray", "singbox"} {
			for _, protocol := range []string{"vless", "hysteria"} {
				t.Run(front+"/"+back+"/"+protocol, func(t *testing.T) {
					var ew, lw panel.NodeConfig
					var uw []panel.User
					read("entry", &ew)
					read("landing", &lw)
					read("users", &uw)
					entry, landing := model.NodeSpecFromPanel(&ew), model.NodeSpecFromPanel(&lw)
					users := model.UserSpecsFromPanel(uw)
					if len(users) != 1 {
						t.Fatal("unexpected exported users")
					}
					landing.ServerPort = runtimeDualPort(t)
					landing.ListenIP = "127.0.0.1"
					landing.Relay.ListenPort = landing.ServerPort
					landing.Relay.Firewall = nil
					// Private targets are deliberately permitted only inside this test.
					landing.CustomRouteRules = []model.CustomRouteRule{
						{Match: model.RouteMatch{DomainSuffixes: []string{"test"}}, Action: model.RouteAction{Type: "direct"}},
						{Match: model.RouteMatch{IPCIDRs: []string{"127.0.0.0/8", "::1/128"}}, Action: model.RouteAction{Type: "direct"}},
					}
					landing.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "freedom", Settings: M{"domainStrategy": "UseIP", "finalRules": []M{{"action": "allow", "ip": []string{"127.0.0.0/8", "::1/128"}}}}}}
					lc := directCompatDNSConfig(t, back)
					lc.CustomOutbound = []M{{"protocol": "freedom", "tag": "direct", "settings": landing.CustomOutbounds[0].Settings}}
					landing.CustomOutbounds = nil
					if back == "singbox" {
						landing.CustomOutbounds = nil
						lc.CustomOutbound = []M{{"type": "direct", "tag": "direct", "domain_resolver": "test-dns"}}
					}
					start := func(node *model.NodeSpec, u []model.UserSpec, cfg config.KernelConfig, cert kernel.TLSCert) kernel.Kernel {
						t.Helper()
						var core kernel.Kernel
						if cfg.Type == "xray" {
							core = xrayKernel.New(cfg)
						} else {
							core = New(cfg)
						}
						if err := model.ValidateNodeSpec(node, cfg); err != nil {
							t.Fatal(err)
						}
						if err := core.Start(node, u, cert); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(core.Stop)
						return core
					}
					landCore := start(landing, nil, lc, kernel.TLSCert{})
					var selected model.RelayChild
					for _, c := range entry.Relay.Children {
						if c.Protocol == "wireguard" && c.RouteID == landing.Relay.RouteID {
							selected = c
							break
						}
					}
					if selected.WireGuard == nil {
						t.Fatal("missing matching snapshot tunnel")
					}
					selected.Port = landing.ServerPort
					// Exercise the outer IPv6 transport as well as inner IPv6 destinations.
					selected.Address = "::1"
					entry.Relay.Children = []model.RelayChild{selected}
					entry.Protocol = protocol
					entry.Network = "tcp"
					entry.NetworkSettings = nil
					entry.ServerPort = runtimeDualPort(t)
					entry.ListenIP = "127.0.0.1"
					entry.TLS = 0
					var cert kernel.TLSCert
					if protocol == "hysteria" {
						entry.Version = 2
						entry.TLS = 1
						cert = runtimeCertificate(t)
					}
					entry.CustomRouteRules = nil
					entry.CustomOutbounds = nil
					entry.Routes = nil
					ec := config.KernelConfig{Type: front, LogLevel: "fatal", ConfigDir: t.TempDir()}
					if front == "xray" {
						ec.CustomRoute = []M{{"type": "field", "ip": []string{"127.0.0.0/8", "::1/128"}, "outboundTag": selected.Tag}}
					} else {
						ec.CustomRoute = []M{{"ip_cidr": []string{"127.0.0.0/8", "::1/128"}, "outbound": selected.Tag}}
					}
					entryCore := start(entry, users, ec, cert)
					selectedUser := users[0]
					selectedUser.UUID = relayCredential(selectedUser, selected.RouteID)
					client := routeTestClient(t, entry, selectedUser)
					for _, ip := range []string{"127.0.0.1", "::1"} {
						tcp, _ := directCompatTCPEcho(t, ip)
						udp, _ := directCompatUDPEcho(t, ip)
						domain := "ipv4-only.test"
						if ip == "::1" {
							domain = "ipv6-only.test"
						}
						for _, domainTarget := range []bool{false, true} {
							tc, uc := tcp, udp
							if domainTarget {
								tc = singM.Socksaddr{Fqdn: domain, Port: tcp.Port}
								uc = singM.Socksaddr{Fqdn: domain, Port: udp.Port}
							}
							c := runtimeDial(t, client, tc)
							runtimeExchange(t, c)
							c.Close()
							p := runtimeUDPDial(t, client, uc)
							directCompatUDPExchange(t, p, uc)
							p.Close()
						}
					}
					// Remove only the selected route while preserving the user and direct grant.
					target, _ := directCompatTCPEcho(t, "127.0.0.1")
					live := runtimeDial(t, client, target)
					runtimeExchange(t, live)
					udpTarget, _ := directCompatUDPEcho(t, "127.0.0.1")
					udpLive := runtimeUDPDial(t, client, udpTarget)
					directCompatUDPExchange(t, udpLive, udpTarget)
					large := bytes.Repeat([]byte("wg-payload"), 13108)
					live.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := live.Write(large); err != nil {
						t.Fatal(err)
					}
					received := make([]byte, len(large))
					if _, err := io.ReadFull(live, received); err != nil || !bytes.Equal(received, large) {
						t.Fatal("large TCP payload mismatch", err)
					}
					packet := bytes.Repeat([]byte("wg-udp"), 300)
					udpLive.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, err := udpLive.WriteTo(packet, udpTarget); err != nil {
						t.Fatal(err)
					}
					if n, _, err := udpLive.ReadFrom(received); err != nil || !bytes.Equal(received[:n], packet) {
						t.Fatal("fragmented UDP mismatch", err)
					}
					want := int64(10*len(runtimePayload) + len(large) + len(packet))
					deadline := time.Now().Add(2 * time.Second)
					for {
						totals, err := entryCore.(kernel.RelayUserTrafficReader).GetRelayUserTraffic(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						pair := totals[users[0].ID][selected.NodeID]
						if pair == [2]int64{want, want} {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("payload accounting got=%v want=%d per direction", pair, want)
						}
						time.Sleep(10 * time.Millisecond)
					}
					revoked := users[0]
					revoked.RelayRoutes = []int{entry.Relay.RouteID}
					if err := entryCore.Reload(entry, []model.UserSpec{revoked}, cert); err != nil {
						t.Fatal(err)
					}
					live.SetDeadline(time.Now().Add(time.Second))
					live.Write([]byte(runtimePayload))
					var b [128]byte
					if n, err := live.Read(b[:]); n > 0 || err == nil {
						t.Fatal("existing TCP survived route revocation")
					}
					live.Close()
					udpLive.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
					udpLive.WriteTo([]byte(runtimePayload), udpTarget)
					if n, _, err := udpLive.ReadFrom(b[:]); n > 0 || err == nil {
						t.Fatal("existing UDP survived route revocation")
					}
					udpLive.Close()
					runtimeReject(t, routeTestClient(t, entry, selectedUser), target)
					traffic, err := entryCore.(kernel.RelayUserTrafficReader).GetRelayUserTraffic(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					if traffic[users[0].ID][selected.NodeID][0] <= 0 || traffic[users[0].ID][selected.NodeID][1] <= 0 {
						t.Fatal("missing selected-route billing")
					}
					for _, routes := range traffic {
						for node := range routes {
							if node != selected.NodeID {
								t.Fatal("traffic charged to wrong route")
							}
						}
					}
					got, _, _, err := landCore.GetUserTraffic(context.Background())
					if err != nil || len(got) != 0 {
						t.Fatal("landing billed twice")
					}
					// Restart both cores with the original panel snapshot and stable keys.
					entryCore.Stop()
					landCore.Stop()
					if err := landCore.Start(landing, nil, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
					if err := entryCore.Start(entry, users, cert); err != nil {
						t.Fatal(err)
					}
					recovered := routeTestClient(t, entry, selectedUser)
					c := runtimeDial(t, recovered, target)
					runtimeExchange(t, c)
					c.Close()
				})
			}
		}
	}
}
