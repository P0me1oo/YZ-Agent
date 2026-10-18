//go:build with_quic

package singbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

// 模拟 Realm 在 TCP 连接前发送 PROXY 头，再经过面板已有的入口到落地链路。
func TestNodeProxyProtocolWithPanelRelay(t *testing.T) {
	for _, entryKind := range []string{"singbox", "xray"} {
		for _, landingKind := range []string{"singbox", "xray"} {
			for _, transit := range []string{"shadowsocks", "vless"} {
				t.Run(fmt.Sprintf("%s/%s/%s", entryKind, landingKind, transit), func(t *testing.T) {
					user := runtimeUser(t, 2601)
					target := runtimeEcho(t)
					landing := runtimeNode(t, transit)
					// 落地开启接收后，入口未附带 PROXY 头的原有内部连接仍须正常认证。
					landing.AcceptProxyProtocol = new(true)
					landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: transit}
					child := model.RelayChild{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: transit,
						Address: "127.0.0.1", Port: landing.ServerPort}
					if transit == "shadowsocks" {
						landing.Relay.Cipher = "2022-blake3-aes-128-gcm"
						landing.Relay.Password = base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 2602).UUID)[:16])
						child.Cipher, child.Password = landing.Relay.Cipher, landing.Relay.Password
					} else {
						landing.Relay.VLESS = &model.RelayVLESSConfig{ID: runtimeUser(t, 2603).UUID}
						child.VLESS = &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}
					}
					landingCore := relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, target.String())
					entry := runtimeNode(t, "vless")
					entry.AcceptProxyProtocol = new(true)
					entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{child}}
					core := relayStartCore(t, entryKind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "wrong-route"))
					clientUser := user
					clientUser.UUID = relayCredential(user, 12)
					destination := singM.ParseSocksaddr("198.51.100.10:80")
					for _, version := range []byte{1, 2} {
						forwarded := *entry
						forwarded.ServerPort = proxyProtocolForwarder(t, entry.ServerPort, version, "203.0.114.99")
						runtimeExchange(t, runtimeDial(t, runtimeClient(t, &forwarded, clientUser), destination))
						_, ips, _, err := core.GetUserTraffic(context.Background())
						if err != nil || !ips[user.ID]["203.0.114.99"] {
							t.Fatal("中转入口未按 PROXY 头记录真实用户来源")
						}
						alive, err := core.(kernel.RelayUserAliveReader).GetRelayUserAlive(context.Background())
						if err != nil || !alive[user.ID][7]["203.0.114.99"] {
							t.Fatal("真实来源未归入所选落地线路")
						}
					}
					// 同一入口继续接受没有 PROXY 头的普通客户端。
					runtimeExchange(t, runtimeDial(t, runtimeClient(t, entry, clientUser), destination))
					want := [2]int64{3 * int64(len(runtimePayload)), 3 * int64(len(runtimePayload))}
					deadline := time.Now().Add(3 * time.Second)
					for {
						traffic, _, _, err := core.GetUserTraffic(context.Background())
						lines, lineErr := core.(kernel.RelayUserTrafficReader).GetRelayUserTraffic(context.Background())
						if err == nil && lineErr == nil && traffic[user.ID] == want && lines[user.ID][7] == want {
							break
						}
						if time.Now().After(deadline) {
							t.Fatalf("中转业务计数错误: user=%v line=%v want=%v", traffic[user.ID], lines[user.ID][7], want)
						}
						time.Sleep(20 * time.Millisecond)
					}
					traffic, _, _, err := landingCore.GetUserTraffic(context.Background())
					if err != nil || len(traffic) != 0 {
						t.Fatal("内部落地不应重复上报面板用户流量")
					}
				})
			}
		}
	}
}
