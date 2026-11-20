//go:build linux

package healthcheck

import (
	"context"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/unix"
)

func TestListenerIPv6WildcardChecksActualIPv4Support(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v6only int
	}{
		{name: "dual stack", v6only: 0},
		{name: "IPv6 only", v6only: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
				var optionErr error
				err := raw.Control(func(fd uintptr) {
					optionErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, tc.v6only)
				})
				if err != nil {
					return err
				}
				return optionErr
			}}
			ln, err := cfg.Listen(t.Context(), "tcp6", "[::]:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
			for _, host := range []string{"0.0.0.0", "127.0.0.1", "::ffff:127.0.0.1", "::", "::1"} {
				wanted := net.JoinHostPort(host, port)
				ipv6Target := host == "::" || host == "::1"
				want := tc.v6only == 0 || ipv6Target
				for range 3 {
					if got, err := Listener(t.Context(), wanted); err != nil || got != want {
						t.Errorf("监听 %s，IPv6-only=%d：got=%v want=%v error=%v", wanted, tc.v6only, got, want, err)
					}
				}
			}
			tcp := ln.(*net.TCPListener)
			if err := tcp.SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			conn, err := tcp.Accept()
			if err == nil {
				conn.Close()
				t.Fatal("监听检查建立了空连接")
			}
			if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatal(err)
			}
			if err := ln.Close(); err != nil {
				t.Fatal(err)
			}
			if got, err := Listener(t.Context(), net.JoinHostPort("0.0.0.0", port)); err != nil || got {
				t.Fatalf("关闭监听后仍被判为正常：%v %v", got, err)
			}
		})
	}
}

func TestListenerConfiguredIPv4WildcardAcceptsGoDualStackListener(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	wanted := net.JoinHostPort("0.0.0.0", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	if got, err := Listener(t.Context(), wanted); err != nil || !got {
		t.Fatalf("配置 %s，实际 %s：got=%v error=%v", wanted, ln.Addr(), got, err)
	}
}

func TestListenerRejectsStaleDualStackSocketRecords(t *testing.T) {
	for _, name := range []string{
		"closed", "regular file", "UDP", "not listening", "different port", "specific IPv6", "different process",
	} {
		t.Run(name, func(t *testing.T) {
			kind := unix.SOCK_STREAM
			if name == "UDP" {
				kind = unix.SOCK_DGRAM
			}
			fd, err := unix.Socket(unix.AF_INET6, kind|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			closed := false
			t.Cleanup(func() {
				if !closed {
					unix.Close(fd)
				}
			})
			if err := unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 0); err != nil {
				t.Fatal(err)
			}
			bound := &unix.SockaddrInet6{}
			if name == "specific IPv6" {
				bound.Addr[15] = 1
			}
			if err := unix.Bind(fd, bound); err != nil {
				t.Fatal(err)
			}
			if kind == unix.SOCK_STREAM && name != "not listening" {
				if err := unix.Listen(fd, 1); err != nil {
					t.Fatal(err)
				}
			}
			address, err := unix.Getsockname(fd)
			if err != nil {
				t.Fatal(err)
			}
			port := address.(*unix.SockaddrInet6).Port
			pid := int32(os.Getpid())
			connection := gnet.ConnectionStat{
				Pid: pid, Fd: uint32(fd), Status: "LISTEN",
				Laddr: gnet.Addr{IP: "::", Port: uint32(port)},
			}
			switch name {
			case "different port":
				port = port%65535 + 1
				connection.Laddr.Port = uint32(port)
			case "different process":
				connection.Pid++
			case "closed":
				if err := unix.Close(fd); err != nil {
					t.Fatal(err)
				}
				closed = true
			case "regular file":
				file, err := os.CreateTemp(t.TempDir(), "reused-fd-")
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				// 保留旧连接表，模拟原编号已被普通文件占用。
				if err := unix.Dup3(int(file.Fd()), fd, unix.O_CLOEXEC); err != nil {
					t.Fatal(err)
				}
			}
			read := func(context.Context, int32) ([]gnet.ConnectionStat, error) {
				return []gnet.ConnectionStat{connection}, nil
			}
			got, err := listener(
				t.Context(),
				net.JoinHostPort("0.0.0.0", strconv.Itoa(port)),
				pid,
				read,
			)
			if err != nil || got {
				t.Fatalf("失效或不匹配的描述符被认作双栈监听：got=%v error=%v", got, err)
			}
		})
	}
}
