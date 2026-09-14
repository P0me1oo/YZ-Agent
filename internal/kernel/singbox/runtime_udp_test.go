package singbox

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestSingBoxRuntimeUDPUserLifecycle(t *testing.T) {
	for _, protocol := range []string{"vmess", "vless", "trojan", "shadowsocks", "shadowsocks2022-128", "shadowsocks2022-256", "mieru", "mieru-udp"} {
		t.Run(protocol, func(t *testing.T) { testRuntimeUDPUserLifecycle(t, protocol) })
	}
}

func testRuntimeUDPUserLifecycle(t *testing.T, protocol string) {
	node := runtimeNode(t, protocol)
	cert := runtimeCertificate(t)
	first, second := runtimeUser(t, 301), runtimeUser(t, 302)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	if err := s.Start(node, []model.UserSpec{first}, cert); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	echo := runtimeUDPEcho(t)
	firstConn := runtimeUDPDial(t, runtimeClient(t, node, first), echo)
	runtimeUDPExchange(t, firstConn, echo)
	if added, err := s.AddUsers([]model.UserSpec{second}); err != nil || added != 1 {
		t.Fatalf("添加 UDP 用户: added=%d, err=%v", added, err)
	}
	secondClient := runtimeClient(t, node, second)
	secondConn := runtimeUDPDial(t, secondClient, echo)
	runtimeUDPExchange(t, secondConn, echo)
	if removed, err := s.RemoveUsers([]model.UserSpec{first}); err != nil || removed != 1 {
		t.Fatalf("删除 UDP 用户: removed=%d, err=%v", removed, err)
	}
	runtimeUDPExchange(t, secondConn, echo)
	runtimeUDPExchange(t, runtimeUDPDial(t, secondClient, echo), echo)
	for range 2 {
		if err := s.Reload(node, []model.UserSpec{second}, cert); err != nil {
			t.Fatal(err)
		}
		runtimeUDPExchange(t, secondConn, echo)
	}
	want := int64(5 * len(runtimePayload))
	runtimeWaitTraffic(t, s, second.ID, [2]int64{want, want})
}

func runtimeUDPEcho(t *testing.T) singM.Socksaddr {
	t.Helper()
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, source, err := listener.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = listener.WriteTo(buffer[:n], source)
		}
	}()
	return singM.ParseSocksaddr(listener.LocalAddr().String())
}

func runtimeUDPDial(t *testing.T, client adapter.Outbound, destination singM.Socksaddr) net.PacketConn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := client.ListenPacket(ctx, destination)
	if err != nil {
		t.Fatalf("连接 UDP 测试目标失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func runtimeUDPExchange(t *testing.T, conn net.PacketConn, destination singM.Socksaddr) {
	t.Helper()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.WriteTo([]byte(runtimePayload), destination.UDPAddr()); err != nil {
		t.Fatalf("UDP 发送失败: %v", err)
	}
	// 留出协议头和加密开销，不能用有效载荷长度限制 UDP 接收缓冲区。
	response := make([]byte, 65535)
	n, _, err := conn.ReadFrom(response)
	if err != nil {
		t.Fatalf("UDP 接收失败: %v", err)
	}
	if !bytes.Equal(response[:n], []byte(runtimePayload)) {
		t.Fatalf("UDP 返回内容不一致: length=%d", n)
	}
}
