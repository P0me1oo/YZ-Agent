package healthcheck

import (
	"context"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func TestListenerMatchesOwnedListeningSocket(t *testing.T) {
	for _, tc := range []struct {
		name, wanted, bound, status string
		port                        uint32
		pid                         int32
		want                        bool
	}{
		{name: "IPv4", wanted: "127.0.0.1:443", bound: "127.0.0.1", status: "LISTEN", port: 443, pid: 7, want: true},
		{name: "IPv4 wildcard", wanted: "127.0.0.1:443", bound: "0.0.0.0", status: "LISTEN", port: 443, pid: 7, want: true},
		{name: "IPv6 wildcard", wanted: "[::1]:443", bound: "::", status: "LISTEN", port: 443, pid: 7, want: true},
		{name: "mapped IPv4", wanted: "127.0.0.1:443", bound: "::ffff:127.0.0.1",
			status: "LISTEN", port: 443, pid: 7, want: true},
		{name: "different process", wanted: "127.0.0.1:443", bound: "127.0.0.1", status: "LISTEN", port: 443, pid: 8},
		{name: "connected socket", wanted: "127.0.0.1:443", bound: "127.0.0.1", status: "ESTABLISHED", port: 443, pid: 7},
		{name: "different address", wanted: "127.0.0.1:443", bound: "192.0.2.1", status: "LISTEN", port: 443, pid: 7},
		{name: "different port", wanted: "127.0.0.1:443", bound: "127.0.0.1", status: "LISTEN", port: 444, pid: 7},
		{name: "IPv6 wildcard is not assumed dual stack", wanted: "127.0.0.1:443", bound: "::",
			status: "LISTEN", port: 443, pid: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(context.Context, int32) ([]gnet.ConnectionStat, error) {
				return []gnet.ConnectionStat{{Pid: tc.pid, Status: tc.status, Laddr: gnet.Addr{IP: tc.bound, Port: tc.port}}}, nil
			}
			got, err := listener(
				t.Context(),
				tc.wanted,
				7,
				read,
			)
			if err != nil || got != tc.want {
				t.Fatalf("监听状态匹配错误：got=%v error=%v", got, err)
			}
		})
	}
}

func TestListenerNativeReadFailureIsNotMissingListener(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("此检查验证 Linux 进程描述符读取错误")
	}
	if _, err := processConnections(t.Context(), 2147483647); err == nil {
		t.Fatal("无法读取进程时不能返回空的监听列表")
	}
}

func TestListenerPropagatesReadFailureAndCancellation(t *testing.T) {
	failure := errors.New("socket table unavailable")
	read := func(context.Context, int32) ([]gnet.ConnectionStat, error) { return nil, failure }
	if ok, err := listener(
		t.Context(),
		"127.0.0.1:443",
		7,
		read,
	); ok || !errors.Is(err, failure) {
		t.Fatalf("读取失败不能判为监听正常：%v %v", ok, err)
	}
	for _, name := range []string{"cancelled before reading", "cancelled while reading"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			if name == "cancelled before reading" {
				cancel()
			}
			read := func(context.Context, int32) ([]gnet.ConnectionStat, error) {
				calls++
				cancel()
				return []gnet.ConnectionStat{{Pid: 7, Status: "LISTEN", Laddr: gnet.Addr{IP: "127.0.0.1", Port: 443}}}, nil
			}
			if ok, err := listener(
				ctx,
				"127.0.0.1:443",
				7,
				read,
			); ok || !errors.Is(err, context.Canceled) {
				t.Fatalf("取消不能返回过期的成功：%v %v", ok, err)
			}
			if name == "cancelled before reading" && calls != 0 {
				t.Fatal("取消后仍读取监听状态")
			}
		})
	}
}

func TestListenerNativeInspectionDoesNotConnectAndDetectsClose(t *testing.T) {
	ln, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	address := ln.Addr().String()
	if runtime.GOOS == "linux" {
		t.Setenv("HOST_PROC", t.TempDir())
	}
	if ok, err := Listener(t.Context(), address); !ok || err != nil {
		t.Fatalf("未检测到当前进程的真实监听：%v %v", ok, err)
	}
	ln.SetDeadline(time.Now().Add(10 * time.Millisecond))
	conn, err := ln.Accept()
	if err == nil {
		conn.Close()
		t.Fatal("状态检查建立了空连接")
	}
	ln.Close()
	if ok, err := Listener(t.Context(), address); ok || err != nil {
		t.Fatalf("已关闭监听仍被认定可用：%v %v", ok, err)
	}
}

func TestUDPListenerOwnership(t *testing.T) {
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := conn.LocalAddr().String()
	if owned, err := ListenerNetwork(t.Context(), "udp", address); err != nil || !owned {
		t.Fatalf("UDP ownership: %v %v", owned, err)
	}
	conn.Close()
	if owned, err := ListenerNetwork(t.Context(), "udp", address); err != nil || owned {
		t.Fatalf("closed UDP: %v %v", owned, err)
	}
}
