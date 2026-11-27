//go:build with_quic && with_wireguard

package singbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/portforward"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xkernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type healthDialer interface {
	ProbeDial(context.Context, string, string, string) (net.Conn, error)
}

// Exercise real TLS/HTTP through the generated core relay outbounds. No public
// network, ports, credentials or running services are reused by this test.
func TestHealthActualChainsFailureAndRecovery(t *testing.T) {
	for _, front := range []string{"xray", "singbox"} {
		for _, back := range []string{"xray", "singbox"} {
			for _, protocol := range []string{"vless", "shadowsocks", "wireguard"} {
				for _, ingress := range []string{"vless", "hysteria2"} {
					t.Run(front+"/"+back+"/"+protocol+"/"+ingress, func(t *testing.T) {
						target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
						defer target.Close()
						roots := x509.NewCertPool()
						roots.AddCert(target.Certificate())
						landing := runtimeNode(t, protocol)
						child := model.RelayChild{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: protocol, Address: "127.0.0.1", Port: landing.ServerPort}
						landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: protocol}
						switch protocol {
						case "wireguard":
							sender, receiver := runtimeWireGuardPair(t)
							landing.Relay.WireGuard = receiver
							child.WireGuard = sender
						case "shadowsocks":
							landing.Relay.Cipher = "2022-blake3-aes-128-gcm"
							landing.Relay.Password = base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 900).UUID)[:16])
							child.Cipher = landing.Relay.Cipher
							child.Password = landing.Relay.Password
						case "vless":
							landing.Relay.VLESS = &model.RelayVLESSConfig{ID: runtimeUser(t, 901).UUID, Network: "tcp", Encryption: "none"}
							child.VLESS = landing.Relay.VLESS
						}
						land := relayStartCore(t, back, landing, nil, kernel.TLSCert{}, target.Listener.Addr().String())
						entry := runtimeNode(t, ingress)
						entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{child}}
						user := runtimeUser(t, 902)
						relayStartCore(t, front, entry, []model.UserSpec{user}, runtimeCertificate(t), target.Listener.Addr().String())
						probe := func(tag string) error {
							ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
							defer cancel()
							selected := user
							route := 11
							if tag != "" {
								route = 12
							}
							selected.UUID = relayCredential(user, route)
							out := map[string]any{"type": "vless", "tag": "health", "server": "127.0.0.1", "server_port": entry.ServerPort, "uuid": selected.UUID}
							if ingress == "hysteria2" {
								delete(out, "uuid")
								out["type"] = "hysteria2"
								out["password"] = selected.UUID
								out["tls"] = map[string]any{"enabled": true, "server_name": "localhost", "insecure": true, "alpn": []string{"h3"}}
							}
							dial, closeClient, err := healthcheck.ForwardClient(ctx, out)
							if err != nil {
								return err
							}
							defer closeClient()
							transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}, DisableKeepAlives: false, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
								return dial(ctx, network, "8.8.8.8:443")
							}}

							defer transport.CloseIdleConnections()
							request, _ := http.NewRequestWithContext(ctx, "GET", "https://example.com/", nil)
							response, err := (&http.Client{Transport: transport}).Do(request)
							if err == nil {
								io.Copy(io.Discard, response.Body)
								response.Body.Close()
								if response.StatusCode != 204 {
									t.Fatal(response.StatusCode)
								}
							}
							return err
						}
						if err := probe(""); err != nil {
							t.Fatal("self outbound", err)
						}
						if err := probe("relay-7"); err != nil {
							t.Fatal("chain", err)
						}
						land.Stop()
						if err := probe("relay-7"); err == nil {
							t.Fatal("offline landing passed")
						}
						if err := probe(""); err != nil {
							t.Fatal("landing outage poisoned front", err)
						}
						if err := land.Start(landing, nil, kernel.TLSCert{}); err != nil {
							t.Fatal(err)
						}
						deadline := time.Now().Add(45 * time.Second)
						for {
							err := probe("relay-7")
							if err == nil {
								break
							}
							if time.Now().After(deadline) {
								t.Fatal("recovery", err)
							}
						}
						// A listening landing cannot hide a broken outbound target.
						target.Close()
						if err := probe("relay-7"); err == nil {
							t.Fatal("broken outbound passed")
						}
					})
				}
			}
		}
	}

}

func TestHealthForwardingUsesProtocolNotOpenPort(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	node := runtimeNode(t, "vless")
	user := runtimeUser(t, 920)
	relayStartCore(t, "singbox", node, []model.UserSpec{user}, kernel.TLSCert{}, target.Listener.Addr().String())
	forward, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	forwardPort := forward.Addr().(*net.TCPAddr).Port
	mode := os.Getenv("YZ_HEALTH_FORWARD_MODE")
	if mode == "realm" {
		forward.Close()
		manager, err := portforward.New(t.TempDir(), "health-real-realm", portforward.TargetPermission{Prefix: netip.MustParsePrefix("127.0.0.1/32"), Ports: []int{node.ServerPort}})
		if err != nil {
			t.Fatal(err)
		}
		defer manager.Close()
		states := manager.Apply(t.Context(), portforward.Policy{Version: 1, Rules: []portforward.Rule{{ID: 1, Mode: "realm", ListenPort: forwardPort, Target: portforward.Endpoint{Address: "127.0.0.1", Port: node.ServerPort}, Network: "tcp"}}})
		if len(states) != 1 || states[0].State != "running" {
			t.Fatal(states)
		}
	} else if mode == "nftables" {
		if os.Getenv("YZ_FORWARD_NETNS_TEST") != "1" {
			t.Fatal("requires isolated netns")
		}
		forward.Close()
		rules := fmt.Sprintf("table ip health_probe { chain output { type nat hook output priority dstnat; policy accept; ip daddr 127.0.0.1 tcp dport %d dnat to 127.0.0.1:%d; }; }\n", forwardPort, node.ServerPort)
		cmd := exec.Command("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(rules)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(output), err)
		}
		defer exec.Command("nft", "delete", "table", "ip", "health_probe").Run()
	} else {
		defer forward.Close()
		go func() {
			for {
				conn, err := forward.Accept()
				if err != nil {
					return
				}
				go func() {
					defer conn.Close()
					remote, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(node.ServerPort)))
					if err != nil {
						return
					}
					defer remote.Close()
					go io.Copy(remote, conn)
					io.Copy(conn, remote)
				}()
			}
		}()
	}
	raw := []byte(fmt.Sprintf(`{"type":"vless","server":"127.0.0.1","server_port":%d,"uuid":%q}`, forwardPort, user.UUID))
	dial, closeClient, err := healthcheck.ForwardClient(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	roots := x509.NewCertPool()
	roots.AddCert(target.Certificate())
	transport := &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{ServerName: "example.com", RootCAs: roots}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	response, err := client.Get("https://8.8.8.8/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	raw = []byte(strings.ReplaceAll(string(raw), user.UUID, runtimeUser(t, 921).UUID))
	badDial, closeBad, err := healthcheck.ForwardClient(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBad()
	transport.DialContext = badDial
	if response, err = client.Get("https://8.8.8.8/"); err == nil {
		response.Body.Close()
		t.Fatal("open forwarding port passed invalid protocol credentials")
	}
}

func TestHealthXrayXHTTPProtocolClient(t *testing.T) {
	node := runtimeNode(t, "vless")
	node.Network = "xhttp"
	node.NetworkSettings = map[string]any{"path": "/diagnostic", "mode": "auto"}
	user := runtimeUser(t, 930)
	target := runtimeEcho(t)
	relayStartCore(t, "xray", node, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
	raw := map[string]any{"protocol": "vless", "tag": "diagnostic", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": node.ServerPort, "users": []any{map[string]any{"id": user.UUID, "encryption": "none"}}}}}, "streamSettings": map[string]any{"network": "xhttp", "xhttpSettings": map[string]any{"path": "/diagnostic", "mode": "auto"}}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dial, closeClient, err := xkernel.DiagnosticClient(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	conn, err := dial(ctx, "tcp", "8.8.8.8:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("diagnostic")); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 10)
	if _, err := io.ReadFull(conn, result); err != nil || string(result) != "diagnostic" {
		t.Fatalf("xhttp handshake %q %v", result, err)
	}
}

func TestHealthWireGuardEndpointClient(t *testing.T) {
	sender, receiver := runtimeWireGuardPair(t)
	target := runtimeEcho(t)
	node := runtimeNode(t, "wireguard")
	node.WireGuard = &panel.WireGuardConfig{PrivateKey: receiver.PrivateKey, Address: receiver.Address, MTU: 1420}
	user := runtimeUser(t, 940)
	user.WireGuard = &panel.WireGuardPeer{PublicKey: receiver.PeerPublicKey, Address: sender.Address}
	relayStartCore(t, "singbox", node, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
	raw := map[string]any{"engine": "singbox-endpoint", "endpoint": map[string]any{"type": "wireguard", "tag": "diagnostic", "address": sender.Address, "private_key": sender.PrivateKey, "mtu": sender.MTU, "peers": []any{map[string]any{"address": "127.0.0.1", "port": node.ServerPort, "public_key": sender.PeerPublicKey, "allowed_ips": []string{"0.0.0.0/0", "::/0"}}}}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	dial, closeClient, err := healthcheck.ForwardClient(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	conn, err := dial(ctx, "tcp", "8.8.8.8:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte("diagnostic")); err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 10)
	if _, err := io.ReadFull(conn, result); err != nil || string(result) != "diagnostic" {
		t.Fatalf("WG endpoint %q %v", result, err)
	}
}
