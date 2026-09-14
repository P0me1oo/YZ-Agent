//go:build with_quic

package singbox

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing-box/adapter"
	boxTLS "github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-quic/hysteria2"
)

// 测试在回环地址启动实际入口和落地；ECH、认证和证书材料均为本次运行生成。
func TestHysteria2ECHRelayRuntime(t *testing.T) {
	for _, entryKind := range []string{"xray", "singbox"} {
		for _, landingKind := range []string{"xray", "singbox"} {
			for _, obfs := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/salamander=%t", entryKind, landingKind, obfs), func(t *testing.T) {
					cert := runtimeCertificate(t)
					configPEM, keyPEM := relayECHKeys(t)
					first, second := runtimeUser(t, 811), runtimeUser(t, 812)
					ss := runtimeNode(t, "shadowsocks")
					ssKey := make([]byte, 16)
					if _, err := rand.Read(ssKey); err != nil {
						t.Fatal(err)
					}
					ss.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
						Password: base64.StdEncoding.EncodeToString(ssKey)}
					vless := runtimeNode(t, "vless")
					vless.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless",
						VLESS: &model.RelayVLESSConfig{ID: relayCredential(runtimeUser(t, 813), 0)}}
					landings := []kernel.Kernel{
						relayStartCore(t, landingKind, ss, nil, kernel.TLSCert{}, relayMarkerEcho(t, "ss")),
						relayStartCore(t, landingKind, vless, nil, kernel.TLSCert{}, relayMarkerEcho(t, "vless")),
					}
					node := runtimeNode(t, "hysteria2")
					node.ServerPort = relayECHPort(t)
					node.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true, "key": keyPEM}}
					if obfs {
						node.Obfs, node.ObfsPassword = "salamander", runtimeUser(t, 814).UUID
					}
					node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
						{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "shadowsocks", Address: "127.0.0.1", Port: ss.ServerPort,
							Cipher: ss.Relay.Cipher, Password: ss.Relay.Password},
						{NodeID: 9, Tag: "relay-9", RouteID: 13, Protocol: "vless", Address: "127.0.0.1", Port: vless.ServerPort,
							VLESS: &model.RelayVLESSConfig{ID: vless.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}},
					}}
					entry := relayStartCore(t, entryKind, node, []model.UserSpec{first}, cert, relayMarkerEcho(t, "direct"))
					relayECHHandshake(t, node, cert, configPEM, true)
					wrongConfig, _ := relayECHKeys(t)
					relayECHHandshake(t, node, cert, wrongConfig, false)
					client := func(user model.UserSpec, route int) adapter.Outbound {
						user.UUID = relayCredential(user, route)
						return runtimeClientWithTLS(t, node, user, map[string]any{
							"enabled": true, "server_name": "localhost", "certificate": []string{string(cert.CertPEM)},
							"ech": map[string]any{"enabled": true, "config": []string{configPEM}},
						})
					}
					want := map[int][2]int64{}
					wantLines := map[int]map[int][2]int64{}
					transfer := func(out adapter.Outbound, user model.UserSpec, nodeID int, marker, network string) {
						relayMarkerExchange(t, out, marker, network)
						delta := [2]int64{int64(len(runtimePayload)), int64(len(runtimePayload) + len(marker) + 1)}
						previous := want[user.ID]
						want[user.ID] = [2]int64{previous[0] + delta[0], previous[1] + delta[1]}
						if nodeID > 0 {
							if wantLines[user.ID] == nil {
								wantLines[user.ID] = map[int][2]int64{}
							}
							previous = wantLines[user.ID][nodeID]
							wantLines[user.ID][nodeID] = [2]int64{previous[0] + delta[0], previous[1] + delta[1]}
						}
					}
					direct, firstSS, firstVLESS := client(first, 11), client(first, 12), client(first, 13)
					for range 2 {
						for _, network := range []string{"tcp", "udp"} {
							transfer(direct, first, 0, "direct", network)
							transfer(firstSS, first, 7, "ss", network)
							transfer(firstVLESS, first, 9, "vless", network)
						}
					}
					if binary := os.Getenv("YZ_HY2_ECH_MIHOMO"); binary != "" {
						for _, route := range []struct {
							id, nodeID int
							marker     string
						}{{12, 7, "ss"}, {13, 9, "vless"}} {
							relayMihomoProbe(t, binary, node, cert, configPEM, relayCredential(first, route.id), func(out adapter.Outbound) {
								for _, network := range []string{"tcp", "udp"} {
									transfer(out, first, route.nodeID, route.marker, network)
								}
							})
						}
						relayMihomoProbe(t, binary, node, cert, wrongConfig, relayCredential(first, 12), func(out adapter.Outbound) {
							relayMarkerReject(t, out, "tcp")
						})
					}
					relayMarkerReject(t, client(second, 12), "tcp")
					for i := range 2 {
						if added, err := entry.AddUsers([]model.UserSpec{second}); err != nil || added != 1-i {
							t.Fatalf("ECH 会话下添加用户失败: added=%d, err=%v", added, err)
						}
					}
					secondSS := client(second, 12)
					for range 2 {
						if err := entry.Reload(node, []model.UserSpec{first, second}, cert); err != nil {
							t.Fatal(err)
						}
						transfer(secondSS, second, 7, "ss", "udp")
					}
					for i := range 2 {
						if removed, err := entry.RemoveUsers([]model.UserSpec{first}); err != nil || removed != 1-i {
							t.Fatalf("ECH 会话下删除用户失败: removed=%d, err=%v", removed, err)
						}
					}
					relayMarkerReject(t, client(first, 13), "tcp")
					transfer(secondSS, second, 7, "ss", "tcp")
					relayECHTraffic(t, entry, want, wantLines)

					// 更换 ECH 密钥和入口端口后，旧公钥必须被拒绝，新公钥仍能选中原落地。
					oldConfig := configPEM
					configPEM, keyPEM = relayECHKeys(t)
					next := *node
					next.ServerPort = relayECHPort(t)
					next.TLSSettings = map[string]any{"ech": map[string]any{"enabled": true, "key": keyPEM}}
					if err := entry.Reload(&next, []model.UserSpec{second}, cert); err != nil {
						t.Fatalf("ECH 密钥轮换后重载失败: %v", err)
					}
					node = &next
					relayECHHandshake(t, node, cert, oldConfig, false)
					relayECHHandshake(t, node, cert, configPEM, true)
					for _, network := range []string{"tcp", "udp"} {
						transfer(client(second, 13), second, 9, "vless", network)
					}
					entry.Stop()
					relayECHTraffic(t, entry, want, wantLines)
					if err := entry.Start(node, []model.UserSpec{second}, cert); err != nil {
						t.Fatalf("ECH 节点停止后恢复失败: %v", err)
					}
					relayECHHandshake(t, node, cert, configPEM, true)
					transfer(client(second, 12), second, 7, "ss", "tcp")
					relayECHTraffic(t, entry, want, wantLines)
					for _, landing := range landings {
						traffic, _, _, err := landing.GetUserTraffic(context.Background())
						if err != nil || len(traffic) != 0 {
							t.Fatalf("落地重复计算用户流量: %v", err)
						}
					}
				})
			}
		}
	}
}

func relayECHKeys(t *testing.T) (string, string) {
	t.Helper()
	configPEM, keyPEM, err := boxTLS.ECHKeygenDefault("public.ech.example")
	if err != nil {
		t.Fatal(err)
	}
	return configPEM, keyPEM
}

func relayECHPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func relayECHHandshake(t *testing.T, node *model.NodeSpec, cert kernel.TLSCert, configPEM string, accepted bool) {
	t.Helper()
	block, _ := pem.Decode([]byte(configPEM))
	if block == nil {
		t.Fatal("无法解析测试 ECH 配置")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert.CertPEM) {
		t.Fatal("无法加载测试证书")
	}
	tlsConfig := &tls.Config{
		ServerName: "localhost", RootCAs: roots, NextProtos: []string{"h3"},
		EncryptedClientHelloConfigList: block.Bytes,
	}
	var rejected atomic.Bool
	if !accepted {
		// 失败用例只验证 ECH 拒绝，避免外层域名证书错误被误认为 ECH 拒绝。
		tlsConfig.EncryptedClientHelloRejectionVerify = func(tls.ConnectionState) error {
			rejected.Store(true)
			return nil
		}
	}
	packets, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	if node.Obfs == "salamander" {
		packets = hysteria2.NewSalamanderConn(packets, []byte(node.ObfsPassword))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := quic.Dial(ctx, packets, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1).To4(), Port: node.ServerPort}, tlsConfig, nil)
	if conn != nil {
		defer conn.CloseWithError(0, "")
	}
	if !accepted {
		if err == nil || !rejected.Load() {
			t.Fatalf("错误公钥必须触发 ECH 拒绝: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("ECH 握手失败: %v", err)
	}
	if !conn.ConnectionState().TLS.ECHAccepted {
		t.Fatal("HY2 QUIC 握手未接受 ECH")
	}
}

func relayECHTraffic(t *testing.T, entry kernel.Kernel, want map[int][2]int64, wantLines map[int]map[int][2]int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		traffic, _, _, err := entry.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if reflect.DeepEqual(traffic, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ECH 重载前后用户流量未恰好累计一次: got=%v, want=%v", traffic, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lines, err := entry.(kernel.RelayUserTrafficReader).GetRelayUserTraffic(context.Background())
	if err != nil || !reflect.DeepEqual(lines, wantLines) {
		t.Fatalf("ECH 中转用户线路计数不匹配: got=%v, want=%v, err=%v", lines, wantLines, err)
	}
	wantTotals := map[int][2]int64{}
	for _, nodes := range wantLines {
		for id, count := range nodes {
			previous := wantTotals[id]
			wantTotals[id] = [2]int64{previous[0] + count[0], previous[1] + count[1]}
		}
	}
	totals, err := entry.(kernel.RelayTrafficReader).GetRelayTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 落地总量沿用核心出站口径；用户及用户线路计数必须与有效载荷完全一致。
	for id, expected := range wantTotals {
		if totals[id][0] < expected[0] || totals[id][1] < expected[1] {
			t.Fatalf("ECH 落地总量漏计: node=%d, got=%v, want至少=%v", id, totals[id], expected)
		}
	}
}
