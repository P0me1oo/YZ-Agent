package singbox

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
)

func TestLeaseCacheExpiryClosesSingboxTCPAndUDP(t *testing.T) {
	remote := &gatetest.Remote{CacheDuration: 300 * time.Millisecond}
	gate := gatetest.Manager(t, remote)
	gate.SetUsers([]int{1})
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"lease-cache-test": 1})
	tracker.deviceGate.Store(gate)
	server, peer := net.Pipe()
	wrapped := tracker.RoutedConnection(t.Context(), server, testInboundContext("lease-cache-test", "8.8.8.8"), nil, nil)
	t.Cleanup(func() { _ = wrapped.Close(); _ = peer.Close() })
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet := tracker.RoutedPacketConnection(t.Context(), &handoverPacket{socket: socket},
		testInboundContext("lease-cache-test", "8.8.8.8"), nil, nil)
	t.Cleanup(func() { _ = packet.Close() })
	if remote.Calls.Load() != 1 {
		t.Fatal("同一许可的 TCP 和 UDP 应本地复用")
	}
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("许可到期没有关闭 TCP：%v", err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := socket.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("许可到期没有关闭 UDP：%v", err)
	}
	// 套接字关闭先唤醒读操作，来源引用在关闭回调返回后才释放。
	deadline := time.Now().Add(time.Second)
	for len(gate.Snapshot().Sources) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(gate.Snapshot().Sources) != 0 {
		t.Fatal("实际关闭后仍保留活跃来源")
	}
}
