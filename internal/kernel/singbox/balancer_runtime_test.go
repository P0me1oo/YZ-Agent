//go:build with_quic && with_utls && with_wireguard

package singbox

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/balancer"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/testing/realitytest"
)

// 真实 Reality 客户端经过调度入口、两台前置和固定落地，覆盖两种核心混用。
func TestBalancedRealityFixedLandingRuntime(t *testing.T) {
	for _, entryKind := range []string{"xray", "singbox"} {
		for _, landingKind := range []string{"xray", "singbox"} {
			for _, transit := range []string{"shadowsocks", "vless", "wireguard"} {
				t.Run(entryKind+"/"+landingKind+"/"+transit, func(t *testing.T) {
					fixture := realitytest.Start(t, true)
					user := runtimeUser(t, 9101)
					landing := runtimeNode(t, transit)
					landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: transit, Cipher: "aes-128-gcm", Password: runtimeUser(t, 9102).UUID}
					if transit == "vless" {
						landing.Relay.VLESS = &model.RelayVLESSConfig{ID: runtimeUser(t, 9103).UUID}
					}
					frontWG, landingWG := balancedWGConfigs(t)
					if transit == "wireguard" {
						landing.Relay.WireGuard = landingWG
					}
					landingCore := relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "fixed"))
					var entries []kernel.Kernel
					var targets []balancer.Target
					for index := range 2 {
						entry := runtimeNode(t, "vless")
						entry.TLS = 2
						entry.Flow = "xtls-rprx-vision"
						entry.AcceptProxyProtocol = new(true)
						entry.BalancerReceiver = &balancer.ReceiverConfig{TrustedAddresses: []string{"127.0.0.1"}}
						entry.TLSSettings = map[string]any{"server_name": "reality.test", "private_key": base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey.Bytes()), "short_id": hex.EncodeToString(fixture.ShortID[:]), "dest": fixture.Config.Dest}
						entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11 + index, Children: []model.RelayChild{{NodeID: 7, Tag: "relay-7", RouteID: 20, Protocol: transit, Address: "127.0.0.1", Port: landing.ServerPort, Cipher: landing.Relay.Cipher, Password: landing.Relay.Password}}}
						if transit == "vless" {
							entry.Relay.Children[0].VLESS = &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}
						}
						if transit == "wireguard" {
							entry.Relay.Children[0].WireGuard = frontWG[index]
						}
						entries = append(entries, relayStartCore(t, entryKind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "wrong-direct")))
						targets = append(targets, balancer.Target{ID: index + 1, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(entry.ServerPort)), Priority: 1, Online: true})
					}
					gateway, err := balancer.Start("127.0.0.1:0", balancer.Config{Network: "tcp", Targets: targets})
					if err != nil {
						t.Fatal(err)
					}
					defer gateway.Close()
					clientUser := user
					clientUser.UUID = relayCredential(user, 20)
					client := shortIDRealityClient(t, gateway.Addr().(*net.TCPAddr).Port, clientUser, fixture.PublicKey, fixture.ShortID, "xtls-rprx-vision")
					for range 4 {
						relayMarkerExchange(t, client, "fixed", "tcp")
					}
					for _, entry := range entries {
						deadline := time.Now().Add(3 * time.Second)
						for {
							traffic, _, _, err := entry.GetUserTraffic(context.Background())
							if err == nil && traffic[user.ID][0] > 0 {
								break
							}
							if time.Now().After(deadline) {
								t.Fatal("新连接没有轮流分配到两台前置")
							}
							time.Sleep(20 * time.Millisecond)
						}
					}
					// 移除一台前置后，新连接继续通向原落地；Reality 中承载的 UDP 也保持正常。
					targets[0].Online = false
					if err := gateway.UpdateTargets(targets); err != nil {
						t.Fatal(err)
					}
					for _, network := range []string{"tcp", "udp"} {
						relayMarkerExchange(t, client, "fixed", network)
					}
					traffic, _, _, err := landingCore.GetUserTraffic(context.Background())
					if err != nil || len(traffic) != 0 {
						t.Fatal("落地不应重复统计用户流量")
					}
				})
			}
		}
	}
}

func balancedWGConfigs(t *testing.T) ([]*model.RelayWireGuardConfig, *model.RelayWireGuardConfig) {
	t.Helper()
	landingKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString
	landing := &model.RelayWireGuardConfig{PrivateKey: enc(landingKey.Bytes()), Address: []string{"10.253.0.2/32"}, MTU: 1380}
	var fronts []*model.RelayWireGuardConfig
	for _, address := range []string{"10.252.0.1/32", "10.252.0.2/32"} {
		key, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		fronts = append(fronts, &model.RelayWireGuardConfig{PrivateKey: enc(key.Bytes()), PeerPublicKey: enc(landingKey.PublicKey().Bytes()), Address: []string{address}, AllowedIPs: []string{"0.0.0.0/0"}, MTU: 1380, Keepalive: 25})
		landing.Peers = append(landing.Peers, model.RelayWireGuardPeer{PublicKey: enc(key.PublicKey().Bytes()), AllowedIPs: []string{address}})
		landing.AllowedIPs = append(landing.AllowedIPs, address)
	}
	landing.PeerPublicKey = landing.Peers[0].PublicKey
	return fronts, landing
}
