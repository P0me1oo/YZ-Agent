//go:build with_quic

package singbox

import (
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	singM "github.com/sagernet/sing/common/metadata"
)

// 入口与落地分别限制来源，例外放行后仍保留原线路和业务路由。
func TestSourcePolicyRelayRuntime(t *testing.T) {
	snapshot, err := sourcepolicy.Parse([]byte("127.0.0.0/8\n2001:db8::/32\n"))
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(n *model.NodeSpec, enabled, allow bool) *model.NodeSpec {
		t.Helper()
		prepared := *n
		n = &prepared
		n.SourcePolicy = &sourcepolicy.Policy{BlockCN: enabled}
		if allow {
			n.SourcePolicy.AllowIPs = []string{"127.0.0.1"}
		}
		n.SourcePolicyReady = enabled
		n.SourceBlockCIDRs, err = n.SourcePolicy.Blocked(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			landing := runtimeNode(t, "shadowsocks")
			landing.Cipher = "aes-128-gcm"
			landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "aes-128-gcm", Password: runtimeUser(t, 1800).UUID}
			landingCore := relayStartCore(t, kind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"))
			entry := runtimeNode(t, "vless")
			entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
				NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "shadowsocks", Address: "127.0.0.1",
				Port: landing.ServerPort, Cipher: landing.Relay.Cipher, Password: landing.Relay.Password,
			}}}
			entry.Routes = []model.RouteRule{{ID: 1, Action: "block", Ports: []string{"6881"}}}
			user := runtimeUser(t, 1801)
			entryCore := relayStartCore(t, kind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
			check := func(route int, pass bool, port string) {
				t.Helper()
				routed := user
				routed.UUID = relayCredential(user, route)
				client := routeTestClient(t, entry, routed)
				marker := "direct"
				if route == 12 {
					marker = "landing"
				}
				for _, network := range []string{"tcp", "udp"} {
					err := routeTestExchange(client, network, singM.ParseSocksaddr("198.51.100.10:"+port), []byte(runtimePayload), marker)
					if pass && err != nil {
						t.Fatalf("线路 %d %s 应可用: %v", route, network, err)
					}
					if !pass && (err == nil || strings.Contains(err.Error(), "应答不匹配")) {
						t.Fatalf("线路 %d %s 应拦截，却收到应答: %v", route, network, err)
					}
				}
			}
			reload := func(core kernel.Kernel, n *model.NodeSpec, users []model.UserSpec) {
				t.Helper()
				if err := core.Reload(n, users, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
			}
			check(11, true, "80")
			check(12, true, "80")
			entry = prepare(entry, true, false)
			reload(entryCore, entry, []model.UserSpec{user})
			check(11, false, "80")
			check(12, false, "80")
			entry = prepare(entry, true, true)
			reload(entryCore, entry, []model.UserSpec{user})
			check(11, true, "80")
			check(12, true, "80")
			check(11, false, "6881")
			landing = prepare(landing, true, false)
			reload(landingCore, landing, nil)
			check(11, true, "80")
			check(12, false, "80")
			landing = prepare(landing, true, true)
			reload(landingCore, landing, nil)
			check(12, true, "80")
		})
	}
}
