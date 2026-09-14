//go:build with_quic

package singbox

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

// 真实启动两种核心，用不同应答标记验证单端口选路；凭据只在测试内存中生成。
func TestSingBoxRelayRuntime(t *testing.T) {
	for _, protocol := range []string{"vless", "hysteria2"} {
		for _, landingKernel := range []string{"singbox", "xray"} {
			t.Run(protocol+"/"+landingKernel, func(t *testing.T) {
				cert := runtimeCertificate(t)
				first, second := runtimeUser(t, 801), runtimeUser(t, 802)
				ss := runtimeNode(t, "shadowsocks")
				ss.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
					Password: base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 803).UUID)[:16])}
				vless := runtimeNode(t, "vless")
				vless.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless",
					VLESS: &model.RelayVLESSConfig{ID: relayCredential(runtimeUser(t, 804), 0)}}
				ssCore := relayStartCore(t, landingKernel, ss, nil, kernel.TLSCert{}, relayMarkerEcho(t, "ss"))
				vlessCore := relayStartCore(t, landingKernel, vless, nil, kernel.TLSCert{}, relayMarkerEcho(t, "vless"))
				node := runtimeNode(t, protocol)
				node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
					{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "shadowsocks", Address: "127.0.0.1", Port: ss.ServerPort,
						Cipher: ss.Relay.Cipher, Password: ss.Relay.Password},
					{NodeID: 9, Tag: "relay-9", RouteID: 13, Protocol: "vless", Address: "127.0.0.1", Port: vless.ServerPort,
						VLESS: &model.RelayVLESSConfig{ID: vless.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}},
				}}
				entry := relayStartCore(t, "singbox", node, []model.UserSpec{first}, cert, relayMarkerEcho(t, "direct")).(*SingBox)
				want := map[int][2]int64{}
				wantLines := map[int]map[int][2]int64{}
				client := func(user model.UserSpec, route int) adapter.Outbound {
					user.UUID = relayCredential(user, route)
					return runtimeClient(t, node, user)
				}
				transfer := func(outbound adapter.Outbound, user model.UserSpec, nodeID int, marker, network string) {
					relayMarkerExchange(t, outbound, marker, network)
					delta := [2]int64{int64(len(runtimePayload)), int64(len(runtimePayload) + len(marker) + 1)}
					value := want[user.ID]
					want[user.ID] = [2]int64{value[0] + delta[0], value[1] + delta[1]}
					if nodeID > 0 {
						if wantLines[user.ID] == nil {
							wantLines[user.ID] = map[int][2]int64{}
						}
						value = wantLines[user.ID][nodeID]
						wantLines[user.ID][nodeID] = [2]int64{value[0] + delta[0], value[1] + delta[1]}
					}
				}
				exchange := func(user model.UserSpec, route, nodeID int, marker, network string) {
					transfer(client(user, route), user, nodeID, marker, network)
				}
				removedSession := client(first, 12)
				transfer(removedSession, first, 7, "ss", "tcp")
				for _, network := range []string{"tcp", "udp"} {
					exchange(first, 11, 0, "direct", network)
					exchange(first, 12, 7, "ss", network)
					exchange(first, 13, 9, "vless", network)
				}
				for i := range 2 {
					if added, err := entry.AddUsers([]model.UserSpec{second, second}); err != nil || added != 1-i {
						t.Fatalf("添加或重复添加用户: %d %v", added, err)
					}
				}
				exchange(second, 13, 9, "vless", "tcp")
				retainedSession := client(second, 12)
				for range 2 {
					if err := entry.Reload(node, []model.UserSpec{first, second}, cert); err != nil {
						t.Fatal(err)
					}
					transfer(retainedSession, second, 7, "ss", "udp")
				}
				if _, err := entry.RemoveUsers([]model.UserSpec{first}); err != nil {
					t.Fatal(err)
				}
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerReject(t, client(first, 12), network)
					relayMarkerReject(t, removedSession, network)
				}
				transfer(retainedSession, second, 7, "ss", "tcp")
				exchange(second, 13, 9, "vless", "udp")
				previousUser := second
				second = runtimeUser(t, previousUser.ID)
				if added, removed, err := entry.UpdateUsers([]model.UserSpec{second}); err != nil || added != 1 || removed != 1 {
					t.Fatalf("轮换凭据失败: added=%d removed=%d err=%v", added, removed, err)
				}
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerReject(t, retainedSession, network)
					relayMarkerReject(t, client(previousUser, 12), network)
				}
				exchange(second, 12, 7, "ss", "tcp")
				invalid := *node
				invalid.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: append([]model.RelayChild{}, node.Relay.Children...)}
				invalid.Relay.Children[0].Tag = "direct"
				if err := entry.Reload(&invalid, []model.UserSpec{second}, cert); err == nil {
					t.Fatal("无效中转配置未被拒绝")
				}
				exchange(second, 12, 7, "ss", "tcp")
				withoutVLESS := *node
				withoutVLESS.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: append([]model.RelayChild{}, node.Relay.Children[:1]...)}
				if err := entry.Reload(&withoutVLESS, []model.UserSpec{second}, cert); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, client(second, 13), "tcp")
				exchange(second, 12, 7, "ss", "udp")
				if err := entry.Reload(node, []model.UserSpec{second}, cert); err != nil {
					t.Fatal(err)
				}
				exchange(second, 13, 9, "vless", "tcp")
				if _, _, err := entry.UpdateUsers(nil); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, client(second, 12), "tcp")
				if _, _, err := entry.UpdateUsers([]model.UserSpec{second}); err != nil {
					t.Fatal(err)
				}
				exchange(second, 12, 7, "ss", "udp")
				for uid, expected := range want {
					runtimeWaitTraffic(t, entry, uid, expected)
				}
				lines, _ := entry.GetRelayUserTraffic(context.Background())
				for uid, nodes := range wantLines {
					for nodeID, expected := range nodes {
						if lines[uid][nodeID] != expected {
							t.Fatalf("用户线路计数错误: uid=%d node=%d got=%v want=%v", uid, nodeID, lines[uid][nodeID], expected)
						}
					}
				}
				totals, _ := entry.GetRelayTraffic(context.Background())
				for _, nodeID := range []int{7, 9} {
					var expected [2]int64
					for _, nodes := range wantLines {
						expected[0] += nodes[nodeID][0]
						expected[1] += nodes[nodeID][1]
					}
					if totals[nodeID] != expected {
						t.Fatalf("落地计数错误: %v != %v", totals[nodeID], expected)
					}
				}
				for _, landing := range []kernel.Kernel{ssCore, vlessCore} {
					traffic, _, _, _ := landing.GetUserTraffic(context.Background())
					if len(traffic) != 0 {
						t.Fatal("落地重复计入用户流量")
					}
				}
				entry.Stop()
				if err := entry.Start(node, []model.UserSpec{second}, cert); err != nil {
					t.Fatal(err)
				}
				exchange(second, 12, 7, "ss", "tcp")
				runtimeWaitTraffic(t, entry, second.ID, want[second.ID])
			})
		}
	}
}

// 实际连接四种共同支持的传输，验证内部出站与两种落地的参数转换。
func TestSingBoxRelayVLESSTransports(t *testing.T) {
	for _, network := range []string{"raw", "ws", "grpc", "httpupgrade"} {
		for _, landingKernel := range []string{"singbox", "xray"} {
			t.Run(network+"/"+landingKernel, func(t *testing.T) {
				settings := map[string]any{"path": "/relay-check", "host": "relay-test.invalid", "serviceName": "relay-check"}
				landing := runtimeNode(t, "vless")
				landing.Network, landing.NetworkSettings = network, settings
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless",
					VLESS: &model.RelayVLESSConfig{ID: relayCredential(runtimeUser(t, 805), 0)}}
				relayStartCore(t, landingKernel, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "transport"))
				node := runtimeNode(t, "vless")
				node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
					NodeID: 9, RouteID: 12, Tag: "relay-9", Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort,
					VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: network, NetworkSettings: settings, Encryption: "none"},
				}}}
				user := runtimeUser(t, 806)
				relayStartCore(t, "singbox", node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
				user.UUID = relayCredential(user, 12)
				client := runtimeClient(t, node, user)
				for _, payloadNetwork := range []string{"tcp", "udp"} {
					relayMarkerExchange(t, client, "transport", payloadNetwork)
				}
			})
		}
	}
}

func relayStartCore(t *testing.T, kind string, node *model.NodeSpec, users []model.UserSpec, cert kernel.TLSCert, target string) kernel.Kernel {
	t.Helper()
	node.CustomRoutes = nil
	cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
	_, port, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	var core kernel.Kernel
	if kind == "singbox" {
		portNumber, _ := strconv.Atoi(port)
		// 通过独立 SOCKS 应答夹具重定向目标，受测节点仍完整使用生成的中转规则。
		proxyNode := runtimeNode(t, "socks")
		proxyNode.CustomRoutes = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": portNumber}}
		proxyUser := runtimeUser(t, 901)
		proxy := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
		if err := proxy.Start(proxyNode, []model.UserSpec{proxyUser}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(proxy.Stop)
		cfg.CustomOutbound = []map[string]any{{"type": "socks", "tag": "direct", "server": "127.0.0.1", "server_port": proxyNode.ServerPort,
			"version": "5", "username": proxyUser.UUID, "password": proxyUser.UUID}}
		core = New(cfg)
	} else {
		cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{
			"redirect": target, "finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}, "port": port}},
		}}}
		core = xray.New(cfg)
	}
	if err := model.ValidateNodeSpec(node, cfg); err != nil {
		t.Fatal(err)
	}
	if err := core.Start(node, users, cert); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	return core
}

func relayMarkerEcho(t *testing.T, marker string) string {
	t.Helper()
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close() })
	udp, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udp.Close() })
	respond := func(payload []byte) []byte { return append([]byte(marker+":"), payload...) }
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, len(runtimePayload))
				for {
					if _, err := io.ReadFull(conn, buffer); err != nil {
						return
					}
					if _, err := conn.Write(respond(buffer)); err != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(respond(buffer[:n]), addr)
		}
	}()
	return tcp.Addr().String()
}

func relayMarkerExchange(t *testing.T, client adapter.Outbound, marker, network string) {
	t.Helper()
	destination := singM.ParseSocksaddr("198.51.100.10:80")
	want := []byte(marker + ":" + runtimePayload)
	if network == "tcp" {
		conn := runtimeDial(t, client, destination)
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, runtimePayload); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("TCP 出口标记不匹配")
		}
	} else {
		conn := runtimeUDPDial(t, client, destination)
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("无法设置 UDP 读取超时: %v", err)
		}
		if _, err := conn.WriteTo([]byte(runtimePayload), destination.UDPAddr()); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 65535)
		n, _, err := conn.ReadFrom(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got[:n], want) {
			t.Fatal(fmt.Sprintf("UDP 出口标记不匹配: len=%d", n))
		}
	}
}

// 拒绝测试只检查是否收到应答，不能把出口标记与普通 echo 不同当作认证失败。
func relayMarkerReject(t *testing.T, client adapter.Outbound, network string) {
	t.Helper()
	checkError := func(err error) {
		if err != nil && strings.Contains(err.Error(), "network changed") {
			t.Fatalf("网络监控未稳定，不能据此判断认证或路由被拒绝: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	destination := singM.ParseSocksaddr("198.51.100.10:80")
	if network == "udp" {
		conn, err := client.ListenPacket(ctx, destination)
		if err != nil {
			checkError(err)
			return
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("无法设置 UDP 读取超时，不能判断是否拒绝请求: %v", err)
		}
		if _, err := conn.WriteTo([]byte(runtimePayload), destination.UDPAddr()); err != nil {
			checkError(err)
			return
		}
		var response [65535]byte
		n, _, err := conn.ReadFrom(response[:])
		if n > 0 {
			t.Fatal("应拒绝的用户或线路仍收到 UDP 出口应答")
		}
		checkError(err)
		return
	}
	conn, err := runtimeConnect(ctx, client, destination)
	if err != nil {
		checkError(err)
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, runtimePayload); err != nil {
		checkError(err)
		return
	}
	var response [1]byte
	n, err := io.ReadFull(conn, response[:])
	if n > 0 {
		t.Fatal("应拒绝的用户或线路仍收到出口应答")
	}
	checkError(err)
}
