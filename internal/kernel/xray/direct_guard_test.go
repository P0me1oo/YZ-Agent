package xray

import (
	"context"
	"net"
	"testing"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type recordingDirectDialer struct {
	internet.Dialer
	targets []xnet.Destination
}

func (d *recordingDirectDialer) Dial(_ context.Context, target xnet.Destination) (stat.Connection, error) {
	d.targets = append(d.targets, target)
	return nil, nil
}

func TestProtectedDirectChecksAddressBeforeDialing(t *testing.T) {
	underlying := &recordingDirectDialer{}
	guard := &protectedDirectDialer{Dialer: underlying}
	for _, address := range []string{"127.0.0.1", "::ffff:10.0.0.1", "fe80::1"} {
		target := xnet.TCPDestination(xnet.IPAddress(net.ParseIP(address)), 443)
		if _, err := guard.Dial(context.Background(), target); err == nil {
			t.Fatalf("本机或内网地址 %s 应被拒绝", address)
		}
	}
	if len(underlying.targets) != 0 {
		t.Fatal("被拒绝的地址不应传给底层拨号器")
	}
	public := xnet.TCPDestination(xnet.IPAddress(net.ParseIP("1.1.1.1")), 443)
	if _, err := guard.Dial(context.Background(), public); err != nil || len(underlying.targets) != 1 || underlying.targets[0] != public {
		t.Fatalf("公网地址应原样传给底层拨号器，错误为 %v", err)
	}
}
