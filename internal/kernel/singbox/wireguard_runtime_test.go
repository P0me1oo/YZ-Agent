//go:build with_quic && with_wireguard

package singbox

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

func runtimeWireGuardPair(t *testing.T) (*model.RelayWireGuardConfig, *model.RelayWireGuardConfig) {
	t.Helper()
	entry, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	landing, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &model.RelayWireGuardConfig{
			PrivateKey: base64.StdEncoding.EncodeToString(entry.Bytes()), PeerPublicKey: base64.StdEncoding.EncodeToString(landing.PublicKey().Bytes()),
			Address: []string{"10.253.0.1/32", "fd7a:797a::1/128"}, AllowedIPs: []string{"0.0.0.0/0", "::/0"}, MTU: 1380, Keepalive: 25,
		}, &model.RelayWireGuardConfig{
			PrivateKey: base64.StdEncoding.EncodeToString(landing.Bytes()), PeerPublicKey: base64.StdEncoding.EncodeToString(entry.PublicKey().Bytes()),
			Address: []string{"10.253.0.2/32", "fd7a:797a::2/128"}, AllowedIPs: []string{"10.253.0.1/32", "fd7a:797a::1/128"}, MTU: 1380,
		}
}

// 真实运行两端核心，覆盖两个入口协议和四种核心组合，凭据只在内存中生成。
func TestWireGuardRelayRuntime(t *testing.T) {
	for _, entryKernel := range []string{"xray", "singbox"} {
		for _, landingKernel := range []string{"xray", "singbox"} {
			for _, protocol := range []string{"vless", "hysteria2"} {
				t.Run(entryKernel+"/"+landingKernel+"/"+protocol, func(t *testing.T) {
					sender, receiver := runtimeWireGuardPair(t)
					landing := runtimeNode(t, "wireguard")
					landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "wireguard", WireGuard: receiver}
					landingCore := relayStartCore(t, landingKernel, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "wg"))
					node := runtimeNode(t, protocol)
					node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
						NodeID: 7, RouteID: 12, Tag: "relay-7", Protocol: "wireguard", Address: "127.0.0.1", Port: landing.ServerPort, WireGuard: sender,
					}}}
					user := runtimeUser(t, 807)
					retained := runtimeUser(t, 808)
					cert := runtimeCertificate(t)
					entry := relayStartCore(t, entryKernel, node, []model.UserSpec{user, retained}, cert, relayMarkerEcho(t, "direct"))
					selected := user
					selected.UUID = relayCredential(user, 12)
					for _, network := range []string{"tcp", "udp"} {
						relayMarkerExchange(t, runtimeClient(t, node, selected), "wg", network)
					}
					for range 2 {
						if err := entry.Reload(node, []model.UserSpec{user, retained}, cert); err != nil {
							t.Fatal(err)
						}
						relayMarkerExchange(t, runtimeClient(t, node, selected), "wg", "tcp")
					}
					if _, err := entry.RemoveUsers([]model.UserSpec{user}); err != nil {
						t.Fatal(err)
					}
					relayMarkerReject(t, runtimeClient(t, node, selected), "tcp")
					if _, err := entry.AddUsers([]model.UserSpec{user}); err != nil {
						t.Fatal(err)
					}
					relayMarkerExchange(t, runtimeClient(t, node, selected), "wg", "udp")
					disabled := *node
					disabled.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, BlockedRouteIDs: []int{12}}
					if err := entry.Reload(&disabled, []model.UserSpec{user, retained}, cert); err != nil {
						t.Fatal(err)
					}
					relayMarkerReject(t, runtimeClient(t, node, selected), "tcp")
					if err := entry.Reload(node, []model.UserSpec{user, retained}, cert); err != nil {
						t.Fatal(err)
					}
					relayMarkerExchange(t, runtimeClient(t, node, selected), "wg", "tcp")
					// 固定落地端口不变的配置重载也必须成功，不能在旧监听关闭前抢占端口。
					reloadedLanding := *landing
					reloadedRelay := *landing.Relay
					reloadedWG := *receiver
					reloadedWG.MTU = 1360
					reloadedRelay.WireGuard = &reloadedWG
					reloadedLanding.Relay = &reloadedRelay
					if err := landingCore.Reload(&reloadedLanding, nil, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
					wireGuardWaitRecovery(t, runtimeClient(t, node, selected))
					landingCore.Stop()
					relayMarkerReject(t, runtimeClient(t, node, selected), "tcp")
					if err := landingCore.Start(landing, nil, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
					wireGuardWaitRecovery(t, runtimeClient(t, node, selected))
				})
			}
		}
	}
}

// 落地重启丢失会话后，WG 需要重新握手；不把首次五秒超时当成恢复失败。
func wireGuardWaitRecovery(t *testing.T, client adapter.Outbound) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	want := []byte("wg:" + runtimePayload)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := client.DialContext(ctx, "tcp", singM.ParseSocksaddr("198.51.100.10:80"))
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = io.WriteString(conn, runtimePayload)
			if err == nil {
				got := make([]byte, len(want))
				_, err = io.ReadFull(conn, got)
				if err == nil && !bytes.Equal(got, want) {
					conn.Close()
					cancel()
					t.Fatal("恢复后出口错误")
				}
			}
			conn.Close()
		}
		cancel()
		if err == nil {
			return
		}
	}
	t.Fatal("WG 落地重启后 45 秒内未恢复")
}

func TestWireGuardMultipleLandingsAndTraffic(t *testing.T) {
	for _, entryKernel := range []string{"xray", "singbox"} {
		t.Run(entryKernel, func(t *testing.T) {
			node := runtimeNode(t, "vless")
			node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11}
			var landings []kernel.Kernel
			for i, kind := range []string{"xray", "singbox"} {
				sender, receiver := runtimeWireGuardPair(t)
				landing := runtimeNode(t, "wireguard")
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "wireguard", WireGuard: receiver}
				landings = append(landings, relayStartCore(t, kind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, kind)))
				node.Relay.Children = append(node.Relay.Children, model.RelayChild{NodeID: 7 + i, RouteID: 12 + i, Tag: "relay-" + strconv.Itoa(7+i), Protocol: "wireguard", Address: "127.0.0.1", Port: landing.ServerPort, WireGuard: sender})
			}
			user := runtimeUser(t, 809)
			entry := relayStartCore(t, entryKernel, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
			for i, marker := range []string{"xray", "singbox"} {
				selected := user
				selected.UUID = relayCredential(user, 12+i)
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerExchange(t, runtimeClient(t, node, selected), marker, network)
				}
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				traffic, _, _, err := entry.GetUserTraffic(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if traffic[user.ID][0] > 0 && traffic[user.ID][1] > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("入口未记录用户流量")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if s, ok := entry.(*SingBox); ok {
				want := [2]int64{4 * int64(len(runtimePayload)), 4*int64(len(runtimePayload)) + int64(2*(len("xray:")+len("singbox:")))}
				runtimeWaitTraffic(t, s, user.ID, want)
				lines, _ := s.GetRelayTraffic(context.Background())
				if lines[7][0] != want[0]/2 || lines[8][0] != want[0]/2 {
					t.Fatal("WG 线路流量归属错误")
				}
			}
			for _, landing := range landings {
				traffic, _, _, err := landing.GetUserTraffic(context.Background())
				if err != nil || len(traffic) != 0 {
					t.Fatal("落地重复计入用户流量")
				}
			}
		})
	}
}
