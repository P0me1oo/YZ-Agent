package xray

import (
	"context"
	"net"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/transport/internet"
	"golang.org/x/sys/unix"
)

// 在 Linux 上检查核心实际创建的已接入 socket，而不是只检查 JSON。
func TestVLESSInboundTCPHealthAppliedToLinuxSocket(t *testing.T) {
	options := parsedInboundSocket(t, &model.NodeSpec{Protocol: "vless", Network: "tcp", ServerPort: 10086})
	listener, err := (&internet.DefaultListener{}).Listen(context.Background(), &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, options)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	raw, err := accepted.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name        string
		level, flag int
		want        int
	}{
		{"探测开关", unix.SOL_SOCKET, unix.SO_KEEPALIVE, 1},
		{"首次探测秒数", unix.IPPROTO_TCP, unix.TCP_KEEPIDLE, 30},
		{"探测间隔秒数", unix.IPPROTO_TCP, unix.TCP_KEEPINTVL, 10},
		{"未确认数据等待毫秒数", unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, 60000},
	} {
		var got int
		var readErr error
		if err := raw.Control(func(fd uintptr) { got, readErr = unix.GetsockoptInt(int(fd), check.level, check.flag) }); err != nil {
			t.Fatal(err)
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got != check.want {
			t.Errorf("%s = %d，预期 %d", check.name, got, check.want)
		}
	}
}
