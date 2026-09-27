//go:build with_quic

package singbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
)

// 真实启动两种内核：BT 握手和 uTP 建连包依靠内核嗅探被拦截，普通流量不受影响；
// 端口和网络两个条件必须同时满足。所有监听都在回环地址，凭据只在测试内存中生成。
func TestRouteConditionsRuntime(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"shadowsocks", "vless"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				web := singM.ParseSocksaddr("198.51.100.10:80")
				torrentPort := singM.ParseSocksaddr("198.51.100.10:6881")

				// 对照组：没有路由时 BT 数据可以正常到达，确认拦截来自路由而不是数据本身。
				open := runtimeNode(t, protocol)
				openUser := runtimeUser(t, 1200)
				relayStartCore(t, kind, open, []model.UserSpec{openUser}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
				openClient := routeTestClient(t, open, openUser)
				routeExpectPass(t, openClient, "tcp", web, routeTestBTHandshake())
				routeExpectPass(t, openClient, "udp", web, routeTestUTPSyn())

				node := runtimeNode(t, protocol)
				node.Routes = []model.RouteRule{
					{ID: 1, Action: "block", Protocols: []string{"bittorrent"}},
					{ID: 2, Action: "block", Ports: []string{"6881"}, Networks: []string{"udp"}},
				}
				user := runtimeUser(t, 1201)
				relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
				client := routeTestClient(t, node, user)

				routeExpectPass(t, client, "tcp", web, []byte(runtimePayload))
				routeExpectPass(t, client, "udp", web, []byte(runtimePayload))
				routeExpectBlocked(t, client, "tcp", web, routeTestBTHandshake())
				routeExpectBlocked(t, client, "udp", web, routeTestUTPSyn())
				// 端口 6881 只拦 UDP，同端口的 TCP 仍然放行。
				routeExpectBlocked(t, client, "udp", torrentPort, []byte(runtimePayload))
				routeExpectPass(t, client, "tcp", torrentPort, []byte(runtimePayload))
				// 被拦截后，同一目标的普通连接仍然正常。
				routeExpectPass(t, client, "tcp", web, []byte(runtimePayload))
			})
		}
	}
}

// 中转入口绑定的路由作用于入口自身用户；经入口转到落地的流量只受落地自己的路由约束。
func TestRouteConditionsRelayEntryRuntime(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			ss := runtimeNode(t, "shadowsocks")
			ss.Cipher = "aes-128-gcm"
			ss.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "aes-128-gcm",
				Password: runtimeUser(t, 1300).UUID}
			relayStartCore(t, kind, ss, nil, kernel.TLSCert{}, relayMarkerEcho(t, "ss"))

			node := runtimeNode(t, "vless")
			node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
				NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "shadowsocks", Address: "127.0.0.1",
				Port: ss.ServerPort, Cipher: ss.Relay.Cipher, Password: ss.Relay.Password,
			}}}
			node.Routes = []model.RouteRule{{ID: 1, Action: "block", Protocols: []string{"bittorrent"}}}
			user := runtimeUser(t, 1301)
			relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
			client := func(route int) adapter.Outbound {
				routed := user
				routed.UUID = relayCredential(user, route)
				return routeTestClient(t, node, routed)
			}
			web := singM.ParseSocksaddr("198.51.100.10:80")

			entryClient := client(11)
			routeExpectPassMarker(t, entryClient, "tcp", web, []byte(runtimePayload), "direct")
			routeExpectPassMarker(t, entryClient, "udp", web, []byte(runtimePayload), "direct")
			routeExpectBlocked(t, entryClient, "tcp", web, routeTestBTHandshake())
			routeExpectBlocked(t, entryClient, "udp", web, routeTestUTPSyn())
			routeExpectPassMarker(t, client(12), "tcp", web, routeTestBTHandshake(), "ss")
			routeExpectPassMarker(t, client(12), "udp", web, routeTestUTPSyn(), "ss")
		})
	}
}

// routeTestClient 与 runtimeClient 相同，但 VLESS 的 UDP 走 XUDP，覆盖多路复用子连接的嗅探。
func routeTestClient(t *testing.T, node *model.NodeSpec, user model.UserSpec) adapter.Outbound {
	t.Helper()
	if node.Protocol != "vless" {
		return runtimeClient(t, node, user)
	}
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{map[string]any{
		"type": "vless", "tag": "probe", "server": "127.0.0.1", "server_port": node.ServerPort,
		"uuid": user.UUID, "packet_encoding": "xudp", "connect_timeout": "5s",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	return client.Outbound().Default()
}

// routeTestBTHandshake 是标准 BitTorrent 握手：协议名、保留位、info hash 和 peer id。
func routeTestBTHandshake() []byte {
	handshake := append([]byte{19}, "BitTorrent protocol"...)
	handshake = append(handshake, make([]byte, 8)...)
	handshake = append(handshake, bytes.Repeat([]byte{0x5a}, 20)...)
	return append(handshake, "-YZ0001-routetest001"...)
}

// routeTestUTPSyn 是不带扩展的 uTP ST_SYN 建连包。
func routeTestUTPSyn() []byte {
	packet := make([]byte, 20)
	packet[0] = 0x41
	binary.BigEndian.PutUint16(packet[2:4], 0x1234)
	binary.BigEndian.PutUint32(packet[4:8], 0x01020304)
	binary.BigEndian.PutUint32(packet[12:16], 0x00100000)
	binary.BigEndian.PutUint16(packet[16:18], 1)
	return packet
}

func routeExpectPass(t *testing.T, client adapter.Outbound, network string, destination singM.Socksaddr, payload []byte) {
	t.Helper()
	routeExpectPassMarker(t, client, network, destination, payload, "direct")
}

func routeExpectPassMarker(t *testing.T, client adapter.Outbound, network string, destination singM.Socksaddr, payload []byte, marker string) {
	t.Helper()
	if err := routeTestExchange(client, network, destination, payload, marker); err != nil {
		t.Fatalf("%s %s 应放行: %v", network, destination, err)
	}
}

func routeExpectBlocked(t *testing.T, client adapter.Outbound, network string, destination singM.Socksaddr, payload []byte) {
	t.Helper()
	err := routeTestExchange(client, network, destination, payload, "direct")
	if err == nil {
		t.Fatalf("%s %s 应被拦截，但收到了应答", network, destination)
	}
	t.Logf("%s %s 已拦截: %v", network, destination, err)
}

// routeTestExchange 发送一次数据并核对应答服务的标记。TCP 应答服务每次读取固定长度，
// 所以只核对第一段；UDP 原样带回整个数据包。
func routeTestExchange(client adapter.Outbound, network string, destination singM.Socksaddr, payload []byte, marker string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if network == "tcp" {
		conn, err := client.DialContext(ctx, "tcp", destination)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(payload); err != nil {
			return err
		}
		want := append([]byte(marker+":"), payload[:len(runtimePayload)]...)
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("TCP 应答不匹配: %q", got)
		}
		return nil
	}
	conn, err := client.ListenPacket(ctx, destination)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.WriteTo(payload, destination.UDPAddr()); err != nil {
		return err
	}
	got := make([]byte, 65535)
	n, _, err := conn.ReadFrom(got)
	if err != nil {
		return err
	}
	if want := append([]byte(marker+":"), payload...); !bytes.Equal(got[:n], want) {
		return errors.New("UDP 应答不匹配")
	}
	return nil
}
