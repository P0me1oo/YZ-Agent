//go:build with_quic

package singbox

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

// 使用真实核心和仅监听回环地址的应答服务，保存旧客户端后撤销单条线路权限。
func TestRelayRoutePermissionsRuntime(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"vless", "hysteria2"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				cert := runtimeCertificate(t)
				first, second := runtimeUser(t, 1101), runtimeUser(t, 1102)
				first.RelayRoutes, second.RelayRoutes = []int{11, 12, 13}, []int{12}
				landing := runtimeNode(t, "shadowsocks")
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
					Password: base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 1103).UUID)[:16])}
				relayStartCore(t, "singbox", landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"))
				node := runtimeNode(t, protocol)
				node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11}
				for _, route := range []int{12, 13} {
					node.Relay.Children = append(node.Relay.Children, model.RelayChild{NodeID: route, Tag: fmt.Sprintf("relay-%d", route), RouteID: route,
						Protocol: "shadowsocks", Address: "127.0.0.1", Port: landing.ServerPort, Cipher: landing.Relay.Cipher, Password: landing.Relay.Password})
				}
				entry := relayStartCore(t, kind, node, []model.UserSpec{first, second}, cert, relayMarkerEcho(t, "direct"))
				client := func(user model.UserSpec, route int) adapter.Outbound {
					user.UUID = relayCredential(user, route)
					return runtimeClient(t, node, user)
				}
				old, kept := client(first, 12), client(first, 13)
				destination := singM.ParseSocksaddr("198.51.100.10:80")
				oldTCP, keptTCP := runtimeDial(t, old, destination), runtimeDial(t, kept, destination)
				oldUDP := runtimeUDPDial(t, old, destination)
				if err := permissionTCPExchange(oldTCP); err != nil {
					t.Fatal(err)
				}
				if err := permissionTCPExchange(keptTCP); err != nil {
					t.Fatal(err)
				}
				if err := permissionUDPExchange(oldUDP, destination); err != nil {
					t.Fatal(err)
				}
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerExchange(t, client(first, 11), "direct", network)
					relayMarkerExchange(t, client(second, 12), "landing", network)
					relayMarkerReject(t, client(second, 11), network)
				}
				first.RelayRoutes = []int{11, 13}
				for range 2 {
					if _, _, err := entry.UpdateUsers([]model.UserSpec{first, second}); err != nil {
						t.Fatal(err)
					}
				}
				if permissionTCPExchange(oldTCP) == nil {
					t.Fatal("撤权后旧 TCP 连接仍能收发")
				}
				if permissionUDPExchange(oldUDP, destination) == nil {
					t.Fatal("撤权后旧 UDP 连接仍能收发")
				}
				if err := permissionTCPExchange(keptTCP); err != nil {
					t.Fatalf("未撤权的旧连接被中断: %v", err)
				}
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerReject(t, old, network)
					relayMarkerReject(t, client(first, 12), network)
					relayMarkerExchange(t, kept, "landing", network)
					relayMarkerExchange(t, client(second, 12), "landing", network)
				}
				if err := entry.Reload(node, []model.UserSpec{first, second}, cert); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, client(first, 12), "tcp")
				first.RelayRoutes = []int{12, 13}
				if _, _, err := entry.UpdateUsers([]model.UserSpec{first, second}); err != nil {
					t.Fatal(err)
				}
				relayMarkerExchange(t, client(first, 12), "landing", "tcp")
				relayMarkerReject(t, client(first, 11), "tcp")
				first.RelayRoutes = []int{}
				if _, _, err := entry.UpdateUsers([]model.UserSpec{first, second}); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, client(first, 13), "tcp")
				relayMarkerExchange(t, client(second, 12), "landing", "tcp")
				entry.Stop()
				if err := entry.Start(node, []model.UserSpec{first, second}, cert); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, client(first, 13), "tcp")
				relayMarkerExchange(t, client(second, 12), "landing", "udp")
			})
		}
	}
}

func permissionTCPExchange(conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, runtimePayload); err != nil {
		return err
	}
	response := make([]byte, len("landing:"+runtimePayload))
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if string(response) != "landing:"+runtimePayload {
		return fmt.Errorf("TCP 回包错误")
	}
	return nil
}

func permissionUDPExchange(conn net.PacketConn, target singM.Socksaddr) error {
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.WriteTo([]byte(runtimePayload), target.UDPAddr()); err != nil {
		return err
	}
	response := make([]byte, 65535)
	n, _, err := conn.ReadFrom(response)
	if err != nil {
		return err
	}
	if string(response[:n]) != "landing:"+runtimePayload {
		return fmt.Errorf("UDP 回包错误")
	}
	return nil
}
