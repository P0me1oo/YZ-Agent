package singbox

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xrayKernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/miekg/dns"
	singM "github.com/sagernet/sing/common/metadata"
)

// 两种内核读取同一份面板出站，使用本地 DNS 验证强制地址族，而非只测试 IP 直连。
func TestLegacyDirectOutboundKernelCompatibility(t *testing.T) {
	for _, mode := range []struct {
		name, kernelType string
		resolveFirst     bool
	}{
		{"xray", "xray", false},
		{"singbox", "singbox", false},
		{"singbox_resolved", "singbox", true},
	} {
		for _, tc := range []struct {
			name, protocol, strategy, source, target, other, otherDomain string
		}{
			{"ipv4", "shadowsocks2022-128", "ForceIPv4", "127.0.0.2", "127.0.0.1", "::1", "ipv6-only.test"},
			{"ipv6", "shadowsocks2022-256", "ForceIPv6", "::1", "::1", "127.0.0.1", "ipv4-only.test"},
		} {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				node := runtimeNode(t, tc.protocol)
				node.CustomRoutes = nil
				node.CustomRouteRules = []model.CustomRouteRule{
					{Match: model.RouteMatch{IPCIDRs: []string{"127.0.0.0/8", "::1/128"}}, Action: model.RouteAction{Type: "route", Target: "direct"}},
					{Match: model.RouteMatch{DomainSuffixes: []string{"test"}}, Action: model.RouteAction{Type: "route", Target: "direct"}},
				}
				if mode.resolveFirst {
					// 路由先解析出两族地址，并优先尝试另一族；出站仍须使用指定源地址。
					preference := "prefer_ipv6"
					if tc.name == "ipv6" {
						preference = "prefer_ipv4"
					}
					node.CustomRouteRules = nil
					node.CustomRoutes = []M{
						{"domain_suffix": []string{"test"}, "action": "resolve", "server": "test-dns", "strategy": preference},
						{"domain_suffix": []string{"test"}, "outbound": "direct"},
						{"ip_cidr": []string{"127.0.0.0/8", "::1/128"}, "outbound": "direct"},
					}
				}
				node.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "freedom", Settings: M{
					"domainStrategy": tc.strategy, "send_through": tc.source,
				}}}
				if mode.kernelType == "xray" {
					// Xray 默认还在 freedom 内拦截私有地址；此例仅放行本机测试目标。
					node.CustomOutbounds[0].Settings["finalRules"] = []M{{
						"action": "allow", "ip": []string{"127.0.0.0/8", "::1/128"},
					}}
				}
				cfg := directCompatDNSConfig(t, mode.kernelType)
				var server kernel.Kernel
				if mode.kernelType == "xray" {
					server = xrayKernel.New(cfg)
				} else {
					server = New(cfg)
				}
				t.Cleanup(server.Stop)
				user := runtimeUser(t, 9201)
				if err := server.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				client := runtimeClient(t, node, user)
				tcpTarget, tcpSources := directCompatTCPEcho(t, tc.target)
				udpTarget, udpSources := directCompatUDPEcho(t, tc.target)
				// 两族目标使用相同端口，错误选中另一族时也可连通，不能靠拒绝连接掩盖回退。
				otherTCP, otherTCPSources := directCompatTCPEchoAt(t, net.JoinHostPort(tc.other, strconv.Itoa(int(tcpTarget.Port))))
				otherUDP, otherUDPSources := directCompatUDPEchoAt(t, net.JoinHostPort(tc.other, strconv.Itoa(int(udpTarget.Port))))
				domainTCP := singM.Socksaddr{Fqdn: "dualstack.test", Port: tcpTarget.Port}
				domainUDP := singM.Socksaddr{Fqdn: "dualstack.test", Port: udpTarget.Port}
				conn := runtimeDial(t, client, domainTCP)
				runtimeExchange(t, conn)
				_ = conn.Close()
				directCompatCheckSource(t, tcpSources, tc.source)
				packetConn := runtimeUDPDial(t, client, domainUDP)
				directCompatUDPExchange(t, packetConn, domainUDP)
				directCompatCheckSource(t, udpSources, tc.source)
				// 已成功转发的会话也不能把后续 UDP 包改发到另一地址族。
				directCompatUDPReject(t, packetConn, otherUDP)
				_ = packetConn.Close()

				// 另一地址族的服务确实在监听，失败不能通过默认出口回退成成功。
				runtimeReject(t, client, otherTCP)
				runtimeReject(t, client, singM.Socksaddr{Fqdn: tc.otherDomain, Port: otherTCP.Port})
				rejected := runtimeUDPDial(t, client, otherUDP)
				directCompatUDPReject(t, rejected, otherUDP)
				_ = rejected.Close()
				select {
				case <-otherTCPSources:
					t.Fatal("不匹配地址族的 TCP 连接使用了默认出口")
				default:
				}
				select {
				case <-otherUDPSources:
					t.Fatal("不匹配地址族的 UDP 数据使用了默认出口")
				default:
				}
			})
		}
	}
}

func directCompatDNSConfig(t *testing.T, kernelType string) config.KernelConfig {
	t.Helper()
	ready := make(chan struct{})
	finished := make(chan error, 1)
	server := &dns.Server{NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		for _, question := range request.Question {
			if question.Qtype == dns.TypeA && question.Name != "ipv6-only.test." {
				response.Answer = append(response.Answer, &dns.A{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.ParseIP("127.0.0.1")})
			}
			if question.Qtype == dns.TypeAAAA && question.Name != "ipv4-only.test." {
				response.Answer = append(response.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60}, AAAA: net.ParseIP("::1")})
			}
		}
		_ = w.WriteMsg(response)
	})}
	var listener io.Closer
	var address net.Addr
	if kernelType == "xray" {
		tcp, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server.Listener, server.Net = tcp, "tcp"
		listener, address = tcp, tcp.Addr()
	} else {
		udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server.PacketConn, server.Net = udp, "udp"
		listener, address = udp, udp.LocalAddr()
	}
	go func() { finished <- server.ActivateAndServe() }()
	t.Cleanup(func() {
		_ = server.Shutdown()
		_ = listener.Close()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("本地 DNS 测试服务未退出")
		}
	})
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("本地 DNS 测试服务未启动")
	}
	port := singM.SocksaddrFromNet(address).Port
	dnsOptions := M{"servers": []M{{"type": "udp", "tag": "test-dns", "server": "127.0.0.1", "server_port": port}}, "final": "test-dns"}
	if kernelType == "xray" {
		// 本地模式避免测试 DNS 自身经过待验证的、可能仅绑定 IPv6 的默认出站。
		dnsOptions = M{"servers": []string{"tcp+local://" + address.String()}, "disableCache": true, "disableFallback": true}
	}
	data, err := json.Marshal(M{"dns": dnsOptions})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "dns.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return config.KernelConfig{Type: kernelType, LogLevel: "error", ConfigDir: directory, CustomConfig: path}
}

func directCompatUDPExchange(t *testing.T, conn net.PacketConn, destination singM.Socksaddr) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.WriteTo([]byte(runtimePayload), destination); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 65535)
	n, _, err := conn.ReadFrom(response)
	if err != nil || !bytes.Equal(response[:n], []byte(runtimePayload)) {
		t.Fatalf("域名 UDP 转发失败: %v", err)
	}
}

func directCompatUDPReject(t *testing.T, conn net.PacketConn, destination singM.Socksaddr) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(400 * time.Millisecond))
	if _, err := conn.WriteTo([]byte(runtimePayload), destination); err != nil {
		return
	}
	response := make([]byte, 65535)
	if _, _, err := conn.ReadFrom(response); err == nil {
		t.Fatal("不匹配地址族的 UDP 请求仍能收到响应")
	}
}
