package service

import (
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/xtls/xray-core/transport/internet"
)

// 面板下发的前置服务器名单同时交给 Xray：名单内来源附带的真实地址被采用，名单外的被忽略。
func TestDeviceIPExcludeListTrustsProxyHeadersFromListedSources(t *testing.T) {
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

	trust.Set([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if got := remoteSeen(); got != "203.0.113.9" {
		t.Fatalf("名单内来源的真实地址应被采用，实际 %s", got)
	}

	trust.Set([]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")})
	if got := remoteSeen(); got != "127.0.0.1" {
		t.Fatalf("名单外来源附带的地址应被忽略，实际 %s", got)
	}
	trust.Set(nil)
	if got := remoteSeen(); got != "127.0.0.1" {
		t.Fatalf("清空名单后旧开关不能重新信任伪造地址，实际 %s", got)
	}
}
