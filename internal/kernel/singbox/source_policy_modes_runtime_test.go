//go:build with_quic

package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestSourcePolicyModesRuntime(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, mode := range []string{"blacklist", "whitelist"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				n := runtimeNode(t, "vless")
				n.SourcePolicy = &sourcepolicy.Policy{Mode: mode, BlockIPs: []string{"127.0.0.1"}, AllowIPs: []string{"198.51.100.7"}}
				n.SourcePolicyReady = true
				var err error
				n.SourceBlockCIDRs, err = n.SourcePolicy.Blocked(sourcepolicy.Snapshot{})
				if err != nil {
					t.Fatal(err)
				}
				user := runtimeUser(t, 1702)
				cert := kernel.TLSCert{}
				core := relayStartCore(t, kind, n, []model.UserSpec{user}, cert, relayMarkerEcho(t, "direct"))
				client := routeTestClient(t, n, user)
				dst := singM.ParseSocksaddr("198.51.100.10:80")
				for _, network := range []string{"tcp", "udp"} {
					routeExpectBlocked(t, client, network, dst, []byte(runtimePayload))
				}
				allowed := *n
				allowed.SourcePolicy = &sourcepolicy.Policy{Mode: mode, AllowIPs: []string{"127.0.0.1"}}
				allowed.SourceBlockCIDRs, err = allowed.SourcePolicy.Blocked(sourcepolicy.Snapshot{})
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := core.Reload(&allowed, []model.UserSpec{user}, cert); err != nil {
						t.Fatal(err)
					}
					client = routeTestClient(t, &allowed, user)
					for _, network := range []string{"tcp", "udp"} {
						routeExpectPass(t, client, network, dst, []byte(runtimePayload))
					}
				}
				if err := core.Reload(n, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
				client = routeTestClient(t, n, user)
				routeExpectBlocked(t, client, "tcp", dst, []byte(runtimePayload))
				disabled := *n
				disabled.SourcePolicy = &sourcepolicy.Policy{Mode: "disabled", BlockCN: true, BlockIPs: []string{"127.0.0.1"}}
				disabled.SourceBlockCIDRs = nil
				if err := core.Reload(&disabled, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
				client = routeTestClient(t, &disabled, user)
				for _, network := range []string{"tcp", "udp"} {
					routeExpectPass(t, client, network, dst, []byte(runtimePayload))
				}
			})
		}
	}
}
