//go:build with_quic

package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	singM "github.com/sagernet/sing/common/metadata"
)

// 用回环来源模拟被拦地区，实际启动两种核心验证 TCP、UDP 和重载恢复。
func TestSourcePolicyRuntime(t *testing.T) {
	snapshot, err := sourcepolicy.Parse([]byte("127.0.0.0/8\n2001:db8::/32\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"xray", "singbox"} {
		for _, protocol := range []string{"vless", "shadowsocks", "hysteria2"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				n := runtimeNode(t, protocol)
				n.SourcePolicy = &sourcepolicy.Policy{BlockCN: true}
				n.SourcePolicyReady = true
				n.SourceBlockCIDRs, err = n.SourcePolicy.Blocked(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				user := runtimeUser(t, 1701)
				cert := kernel.TLSCert{}
				if protocol == "hysteria2" {
					cert = runtimeCertificate(t)
				}
				core := relayStartCore(t, kind, n, []model.UserSpec{user}, cert, relayMarkerEcho(t, "direct"))
				client := routeTestClient(t, n, user)
				dst := singM.ParseSocksaddr("198.51.100.10:80")
				for _, network := range []string{"tcp", "udp"} {
					routeExpectBlocked(t, client, network, dst, []byte(runtimePayload))
				}
				allowed := *n
				allowed.SourcePolicy = &sourcepolicy.Policy{BlockCN: true, AllowIPs: []string{"127.0.0.1"}}
				allowed.SourceBlockCIDRs, err = allowed.SourcePolicy.Blocked(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				for range 2 {
					if err := core.Reload(&allowed, []model.UserSpec{user}, cert); err != nil {
						t.Fatal(err)
					}
					// 验证重载后的新连接，不沿用客户端缓存的旧 QUIC 会话。
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
				disabled.SourcePolicy, disabled.SourceBlockCIDRs = nil, nil
				disabled.SourcePolicyReady = false
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
