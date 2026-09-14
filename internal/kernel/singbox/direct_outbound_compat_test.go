package singbox

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

func mustBuildConfig(t testing.TB, cfg config.KernelConfig, node *model.NodeSpec, users []model.UserSpec, cert kernel.TLSCert) M {
	t.Helper()
	result, err := buildConfig(cfg, node, users, cert)
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	return result
}

// 使用独立测试身份和回环地址，验证原有面板参数能启动并实际绑定出口。
func TestSingBoxLegacyDirectOutboundRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, strategy, source, target string
	}{
		{"ipv4", "ForceIPv4", "127.0.0.2", "127.0.0.1"},
		{"ipv6", "ForceIPv6", "::1", "::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := runtimeNode(t, "shadowsocks2022-128")
			node.CustomRoutes = []map[string]any{{"ip_cidr": []string{"127.0.0.0/8", "::1/128"}, "outbound": "direct"}}
			node.CustomOutbounds = []model.OutboundConfig{{
				Tag: "direct", Protocol: "direct", Settings: map[string]any{
					"domainStrategy": tc.strategy, "send_through": tc.source,
				},
			}}
			user := runtimeUser(t, 9101)
			users := []model.UserSpec{user}
			s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			t.Cleanup(s.Stop)
			if err := s.Start(node, nil, kernel.TLSCert{}); err != nil {
				t.Fatalf("原有面板出站配置启动失败: %v", err)
			}
			if _, _, err := s.UpdateUsers(users); err != nil {
				t.Fatalf("应用首批用户失败: %v", err)
			}
			client := runtimeClient(t, node, user)
			tcpTarget, tcpSources := directCompatTCPEcho(t, tc.target)
			udpTarget, udpSources := directCompatUDPEcho(t, tc.target)
			for range 2 {
				conn := runtimeDial(t, client, tcpTarget)
				runtimeExchange(t, conn)
				_ = conn.Close()
				directCompatCheckSource(t, tcpSources, tc.source)
				packetConn := runtimeUDPDial(t, client, udpTarget)
				runtimeUDPExchange(t, packetConn, udpTarget)
				_ = packetConn.Close()
				directCompatCheckSource(t, udpSources, tc.source)
				if err := s.Reload(node, users, kernel.TLSCert{}); err != nil {
					t.Fatalf("重复重载失败: %v", err)
				}
			}
			s.Stop()
			if err := s.Start(node, users, kernel.TLSCert{}); err != nil {
				t.Fatalf("停止后恢复失败: %v", err)
			}
			conn := runtimeDial(t, client, tcpTarget)
			runtimeExchange(t, conn)
			_ = conn.Close()
			directCompatCheckSource(t, tcpSources, tc.source)
			want := int64(5 * len(runtimePayload))
			runtimeWaitTraffic(t, s, user.ID, [2]int64{want, want})
		})
	}
}

func directCompatTCPEcho(t *testing.T, address string) (singM.Socksaddr, <-chan string) {
	t.Helper()
	return directCompatTCPEchoAt(t, net.JoinHostPort(address, "0"))
}

func directCompatTCPEchoAt(t *testing.T, address string) (singM.Socksaddr, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sources := make(chan string, 32)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			sources <- conn.RemoteAddr().(*net.TCPAddr).IP.String()
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return singM.ParseSocksaddr(listener.Addr().String()), sources
}

func directCompatUDPEcho(t *testing.T, address string) (singM.Socksaddr, <-chan string) {
	t.Helper()
	return directCompatUDPEchoAt(t, net.JoinHostPort(address, "0"))
}

func directCompatUDPEchoAt(t *testing.T, address string) (singM.Socksaddr, <-chan string) {
	t.Helper()
	listener, err := net.ListenPacket("udp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	sources := make(chan string, 32)
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, source, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			sources <- source.(*net.UDPAddr).IP.String()
			_, _ = listener.WriteTo(buffer[:n], source)
		}
	}()
	return singM.ParseSocksaddr(listener.LocalAddr().String()), sources
}

func directCompatCheckSource(t *testing.T, sources <-chan string, want string) {
	t.Helper()
	select {
	case got := <-sources:
		if got != want {
			t.Fatalf("出口地址错误: got=%s, want=%s", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到测试连接的出口地址")
	}
}
