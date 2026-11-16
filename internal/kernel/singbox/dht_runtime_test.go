//go:build integration && with_quic

package singbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

func dhtPing() []byte {
	return []byte("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe")
}

func dhtMessages() [][]byte {
	return [][]byte{
		dhtPing(),
		[]byte("d1:ad2:id20:012345678901234567899:info_hash20:012345678901234567894:wantl2:n46:futureee1:q9:get_peers1:t2:aa1:y1:qe"),
		[]byte("d1:ad2:id20:012345678901234567896:target20:01234567890123456789e1:q9:find_node1:t2:aa1:y1:qe"),
		[]byte("d1:ad2:id20:012345678901234567899:info_hash20:012345678901234567896:scrapei1e4:wantl2:n42:n6ee1:q9:get_peers2:roi1e1:t2:aa1:y1:qe"),
		[]byte("d1:rd2:id20:01234567890123456789e1:t2:aa1:y1:re"),
		[]byte("d1:eli203e14:Protocol Errore1:t2:aa1:y1:ee"),
		[]byte("d1:ad2:id20:012345678901234567896:target20:01234567890123456789e1:q3:get1:t2:aa1:y1:qe"),
		[]byte("d1:ad2:id20:012345678901234567895:token5:token1:v5:valuee1:q3:put1:t2:aa1:y1:qe"),
		[]byte("d1:ad2:id20:012345678901234567896:target20:01234567890123456789e1:q17:sample_infohashes1:t2:aa1:y1:qe"),
	}
}

// 同一条真实 UDP 会话先通过普通报文，再逐个检查 DHT 和后续普通报文。
// 改变目标端口验证 XUDP/多目标传输按每份报文的目的地址重新匹配规则。
func TestDHTRuntimeSessionConditionsAndLogs(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"shadowsocks", "vless"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				destination := singM.ParseSocksaddr(relayMarkerEcho(t, "direct"))
				other := singM.ParseSocksaddr(relayMarkerEcho(t, "direct"))
				log := &accesslog.Recorder{NodeID: 31}
				log.Enable(true)
				node := runtimeNode(t, protocol)
				node.Routes = []model.RouteRule{{
					ID: 901, Action: "block", Protocols: []string{"bittorrent"},
					Match: []string{"127.0.0.1/32"}, Ports: []string{fmt.Sprint(destination.Port)}, Networks: []string{"udp"},
				}}
				user := runtimeUser(t, 2201)
				dhtStartNative(t, kind, node, user, log)
				client := routeTestClient(t, node, user)
				conn := dhtSession(t, client, destination)
				dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
				for i, message := range dhtMessages() {
					t.Run(fmt.Sprint(i), func(t *testing.T) {
						dhtExchange(t, conn, destination, message, "direct", true)
						dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
					})
				}
				// 非目标端口、非目标地址、截断/伪造 KRPC 和普通 UDP 都继续通过。
				dhtExchange(t, conn, other, dhtPing(), "direct", false)
				for _, message := range [][]byte{
					dhtPing()[:len(dhtPing())-1], append(dhtPing(), 'x'),
					[]byte(`{"q":"ping","t":"aa","y":"q"}`),
					[]byte("d1:ad2:id5:shorte1:q4:ping1:t2:aa1:y1:qe"),
				} {
					dhtExchange(t, conn, destination, message, "direct", false)
				}
				records := accessRecords(t, log, 1)
				if len(records) != 1 {
					t.Fatalf("同会话同目标重复记录: %+v", records)
				}
				r := records[0]
				if r.UserID != user.ID || r.RuleID != 901 || r.Port != destination.Port || r.Network != "udp" || r.Destination != "127.0.0.1" {
					t.Fatalf("拦截归属错误: %+v", r)
				}
				// 无 UDP 条件命中时不得误拦，TCP 里的同样字节也保持原行为。
				routeExpectPass(t, client, "tcp", destination, dhtPing())
				log.Enable(false)
				routeExpectBlocked(t, client, "udp", destination, dhtPing())
				if batch, _ := log.Next(); batch != nil {
					t.Fatal("关闭采集后仍记录 DHT 拦截")
				}
			})
		}
	}
}

func TestDHTRuntimeUnboundAndRuleOrder(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, mode := range []string{"unbound", "tcp-only", "other-address", "allow-first"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				node := runtimeNode(t, "vless")
				if mode != "unbound" {
					network := "udp"
					if mode == "tcp-only" {
						network = "tcp"
					}
					node.Routes = []model.RouteRule{{ID: 910, Action: "block", Protocols: []string{"bittorrent"}, Networks: []string{network}}}
				}
				if mode == "allow-first" {
					node.Routes = append([]model.RouteRule{{ID: 909, Action: "direct", Match: []string{"198.51.100.10/32"}}}, node.Routes...)
				}
				if mode == "other-address" {
					node.Routes[0].Match = []string{"198.51.100.11/32"}
				}
				log := &accesslog.Recorder{NodeID: 31}
				log.Enable(true)
				user := runtimeUser(t, 2210)
				relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), nil, log)
				client := routeTestClient(t, node, user)
				destination := singM.ParseSocksaddr("198.51.100.10:6881")
				conn := dhtSession(t, client, destination)
				for _, message := range dhtMessages() {
					dhtExchange(t, conn, destination, message, "direct", false)
				}
				if batch, _ := log.Next(); batch != nil {
					t.Fatalf("未命中 BT 拦截规则却记录拦截: %+v", batch)
				}
			})
		}
	}
}

func TestDHTRuntimeMultiplex(t *testing.T) {
	destination := singM.ParseSocksaddr(relayMarkerEcho(t, "direct"))
	node := runtimeNode(t, "vless")
	node.Multiplex = &model.MultiplexConfig{Enabled: true}
	node.Routes = []model.RouteRule{{ID: 932, Action: "block", Protocols: []string{"bittorrent"},
		Match: []string{"127.0.0.1/32"}, Ports: []string{fmt.Sprint(destination.Port)}, Networks: []string{"udp"}}}
	log := &accesslog.Recorder{NodeID: 31}
	log.Enable(true)
	user := runtimeUser(t, 2401)
	dhtStartNative(t, "singbox", node, user, log)
	client := routeTestClient(t, node, user)
	first, second := dhtSession(t, client, destination), dhtSession(t, client, destination)
	dhtExchange(t, first, destination, []byte(runtimePayload), "direct", false)
	dhtExchange(t, second, destination, []byte(runtimePayload), "direct", false)
	dhtExchange(t, first, destination, dhtPing(), "direct", true)
	dhtExchange(t, second, destination, append(dhtPing(), 'x'), "direct", false)
	dhtExchange(t, second, destination, dhtPing(), "direct", true)
	dhtExchange(t, first, destination, []byte(runtimePayload), "direct", false)
	dhtExchange(t, second, destination, []byte(runtimePayload), "direct", false)
	records := accessRecords(t, log, 2)
	if len(records) != 2 {
		t.Fatalf("多路复用子会话记录错误: %+v", records)
	}
	for _, record := range records {
		if record.UserID != user.ID || record.RuleID != 932 || record.Port != destination.Port {
			t.Fatalf("多路复用归属错误: %+v", record)
		}
	}
}

func TestDHTRuntimeReload(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			node := runtimeNode(t, "vless")
			node.Routes = []model.RouteRule{{ID: 940, Action: "block", Protocols: []string{"bittorrent"}}}
			user := runtimeUser(t, 2402)
			log := &accesslog.Recorder{NodeID: 31}
			log.Enable(true)
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), nil, log)
			destination := singM.ParseSocksaddr("198.51.100.10:6881")
			for rule := 940; rule <= 941; rule++ {
				if rule == 941 {
					changed := *node
					changed.Routes = []model.RouteRule{{ID: rule, Action: "block", Protocols: []string{"bittorrent"}}}
					node = &changed
					if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
				}
				client := routeTestClient(t, node, user)
				conn := dhtSession(t, client, destination)
				dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
				dhtExchange(t, conn, destination, dhtPing(), "direct", true)
				if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
				records := accessRecords(t, log, 1)
				if len(records) != 1 || records[0].RuleID != rule || records[0].UserID != user.ID {
					t.Fatalf("重载后记录错误: %+v", records)
				}
			}
		})
	}
}

func dhtStartNative(t *testing.T, kind string, node *model.NodeSpec, user model.UserSpec, log *accesslog.Recorder) kernel.Kernel {
	t.Helper()
	cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
	var core kernel.Kernel
	if kind == "singbox" {
		// 回环测试需在内置私网保护之前放行夹具，并先放置同条件的 BT 规则。
		// 仍使用生产路由编译器和同一个拦截出站，避免夹具的通用放行覆盖受测规则。
		node.CustomRoutes = []map[string]any{{"action": "sniff", "sniffer": []string{"bittorrent"}}}
		for _, rule := range compilePanelRouteRule(node.Routes[0]) {
			node.CustomRoutes = append(node.CustomRoutes, rule)
		}
		node.CustomRoutes = append(node.CustomRoutes, map[string]any{"ip_cidr": []string{"127.0.0.1/32"}, "outbound": "direct"})
		core = New(cfg)
	} else {
		cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{
			"finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}}},
		}}}
		node.CustomRoutes = []map[string]any{
			{"type": "field", "protocol": []string{"bittorrent"}, "network": "udp", "ip": []string{"127.0.0.1/32"},
				"port": node.Routes[0].Ports[0], "outboundTag": model.PanelBlockTag(node.Routes[0].ID)},
			{"type": "field", "ip": []string{"127.0.0.1/32"}, "outboundTag": "direct"},
		}
		core = xray.New(cfg)
	}
	core.(interface{ SetBlockLog(*accesslog.Recorder) }).SetBlockLog(log)
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	return core
}

func TestDHTRuntimeRelay(t *testing.T) {
	for _, entryKind := range []string{"singbox", "xray"} {
		for _, landingKind := range []string{"singbox", "xray"} {
			for _, blockedAt := range []string{"entry", "landing"} {
				t.Run(entryKind+"/"+landingKind+"/"+blockedAt, func(t *testing.T) {
					entryLog, landingLog := &accesslog.Recorder{NodeID: 3}, &accesslog.Recorder{NodeID: 7}
					entryLog.Enable(true)
					landingLog.Enable(true)
					landing := runtimeNode(t, "vless")
					landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: runtimeUser(t, 2300).UUID}}
					if blockedAt == "landing" {
						landing.Routes = []model.RouteRule{{ID: 921, Action: "block", Protocols: []string{"bittorrent"}}}
					}
					relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"), nil, landingLog)
					entry := runtimeNode(t, "vless")
					entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
						NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort,
						VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"},
					}}}
					if blockedAt == "entry" {
						entry.Routes = []model.RouteRule{{ID: 920, Action: "block", Protocols: []string{"bittorrent"}}}
					}
					user := runtimeUser(t, 2301)
					relayStartCore(t, entryKind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), nil, entryLog)
					destination := singM.ParseSocksaddr("198.51.100.10:6881")
					routed := user
					routed.UUID = relayCredential(user, 12)
					client := routeTestClient(t, entry, routed)
					conn := dhtSession(t, client, destination)
					dhtExchange(t, conn, destination, []byte(runtimePayload), "landing", false)
					blocked := blockedAt == "landing"
					dhtExchange(t, conn, destination, dhtPing(), "landing", blocked)
					dhtExchange(t, conn, destination, []byte(runtimePayload), "landing", false)
					if blocked {
						records := accessRecords(t, landingLog, 1)
						if len(records) != 1 || records[0].RuleID != 921 || records[0].UserID != 0 {
							t.Fatalf("落地身份或规则错误: %+v", records)
						}
					} else {
						// 入口路由只约束入口自身出网，不能覆盖用户所选落地。
						direct := user
						direct.UUID = relayCredential(user, 11)
						directConn := dhtSession(t, routeTestClient(t, entry, direct), destination)
						dhtExchange(t, directConn, destination, []byte(runtimePayload), "direct", false)
						dhtExchange(t, directConn, destination, dhtPing(), "direct", true)
						records := accessRecords(t, entryLog, 1)
						if len(records) != 1 || records[0].RuleID != 920 || records[0].UserID != user.ID {
							t.Fatalf("入口身份或规则错误: %+v", records)
						}
					}
				})
			}
		}
	}
}

func dhtSession(t *testing.T, client adapter.Outbound, destination singM.Socksaddr) net.PacketConn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := client.ListenPacket(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func dhtExchange(t *testing.T, conn net.PacketConn, destination singM.Socksaddr, payload []byte, marker string, blocked bool) {
	t.Helper()
	if _, err := conn.WriteTo(payload, destination.UDPAddr()); err != nil {
		t.Fatal(err)
	}
	wait := 2 * time.Second
	if blocked {
		wait = 350 * time.Millisecond
	}
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, _, err := conn.ReadFrom(buffer)
	if blocked {
		if err == nil {
			t.Fatalf("DHT 报文到达应答服务: %q", buffer[:n])
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte(strings.TrimSpace(marker)+":"), payload...)
	if !bytes.Equal(buffer[:n], want) {
		t.Fatalf("UDP 应答不匹配: got %q want %q", buffer[:n], want)
	}
}

func TestDHTRuntimeFirstPacketKeepsOrdinaryUDP(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			destination := singM.ParseSocksaddr(relayMarkerEcho(t, "direct"))
			node := runtimeNode(t, "vless")
			node.Routes = []model.RouteRule{{ID: 991, Action: "block", Protocols: []string{"bittorrent"}, Match: []string{"127.0.0.1/32"}, Ports: []string{fmt.Sprint(destination.Port)}, Networks: []string{"udp"}}}
			recorder := &accesslog.Recorder{NodeID: 31}
			recorder.Enable(true)
			user := runtimeUser(t, 2991)
			dhtStartNative(t, kind, node, user, recorder)
			conn := dhtSession(t, routeTestClient(t, node, user), destination)
			for range 2 {
				dhtExchange(t, conn, destination, dhtPing(), "direct", true)
				dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
			}
			records := accessRecords(t, recorder, 1)
			if len(records) != 1 || records[0].RuleID != 991 || records[0].UserID != user.ID {
				t.Fatalf("first DHT log or identity incorrect: %+v", records)
			}
		})
	}
}
