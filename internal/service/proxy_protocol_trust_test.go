package service

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/transport/internet"
)

// 新旧配置均按布尔开关接收，不以统计排除名单决定信任范围。
func TestProxyProtocolBooleanSwitchControlsTrustedHeaders(t *testing.T) {
	trust := &internet.ProxyProtocolTrust{}
	ctx := internet.ContextWithProxyProtocolTrust(context.Background(), trust)
	listener, err := internet.ListenSystem(ctx, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, &internet.SocketConfig{AcceptProxyProtocol: true})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	remoteSeen := func() string {
		t.Helper()
		go func() {
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, "PROXY TCP4 203.0.113.9 127.0.0.1 40000 443\r\nhello")
			_ = conn.(*net.TCPConn).CloseWrite()
			_, _ = io.Copy(io.Discard, conn)
		}()
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		remote := conn.RemoteAddr().(*net.TCPAddr).IP.String()
		_, _ = io.ReadAll(conn)
		return remote
	}

	for _, node := range []*model.NodeSpec{
		{AcceptProxyProtocol: new(true)},
		{AcceptProxyProtocol: new(false), NetworkSettings: map[string]any{"acceptProxyProtocol": true}},
		{DeviceIPExclude: []string{"127.0.0.1"}},
		{NetworkSettings: map[string]any{"acceptProxyProtocol": true}},
	} {
		trust.Set(kernel.ProxyProtocolPrefixes(node.ProxyProtocolSetting()))
		want := "127.0.0.1"
		if node.GetProxyProtocol() {
			want = "203.0.113.9"
		}
		if got := remoteSeen(); got != want {
			t.Fatalf("开关与接收行为不一致: got=%s want=%s", got, want)
		}
	}
}
