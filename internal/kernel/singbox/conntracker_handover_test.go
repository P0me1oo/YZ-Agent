package singbox

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
	N "github.com/sagernet/sing/common/network"
)

type handoverPacket struct {
	N.PacketConn
	socket net.PacketConn
}

func (p *handoverPacket) Close() error { return p.socket.Close() }

func TestDeviceHandoverClosesSingboxTCPAndUDPOnlyForSelectedAccount(t *testing.T) {
	gate := gatetest.Manager(t, &gatetest.Remote{})
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"handover-one": 1, "handover-two": 2})
	tracker.deviceGate.Store(gate)
	open := func(identity string) (net.Conn, net.Conn) {
		server, peer := net.Pipe()
		wrapped := tracker.RoutedConnection(t.Context(), server, testInboundContext(identity, "8.8.8.8"), nil, nil)
		t.Cleanup(func() { _ = wrapped.Close(); _ = peer.Close() })
		return wrapped, peer
	}
	_, oldPeer := open("handover-one")
	other, otherPeer := open("handover-two")
	socket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	packet := tracker.RoutedPacketConnection(t.Context(), &handoverPacket{socket: socket},
		testInboundContext("handover-one", "8.8.8.8"), nil, nil)
	t.Cleanup(func() { _ = packet.Close() })
	source := gatetest.Source(t, gate, 1, "8.8.8.8")
	gate.ApplyRevocations([]devicegate.Revocation{{UserID: 1, IP: source.IP, Lease: source.Lease}})
	_ = oldPeer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := oldPeer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("旧 TCP 连接未关闭：%v", err)
	}
	_ = socket.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := socket.ReadFrom(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("旧 UDP 套接字未关闭：%v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := otherPeer.Write([]byte("ok")); done <- err }()
	_ = other.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := other.Read(make([]byte, 2)); err != nil {
		t.Fatal("其它账号的连接受到影响")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	traffic, alive, count := tracker.GetUserTraffic()
	if count != 1 || len(alive[1]) != 0 || len(alive[2]) != 1 || len(gate.Snapshot().Sources) != 1 || traffic[2][0] != 2 {
		t.Fatal("关闭后来源、连接或流量统计错误")
	}
}

func TestDeviceHandoverPendingSingboxAdmissionIsNotReportedAsOnline(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	remote := &gatetest.Remote{Admit: func(ctx context.Context, _ devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
		close(started)
		select {
		case <-ctx.Done():
			return devicegate.AdmissionReply{}, ctx.Err()
		case <-finish:
		}
		return devicegate.AdmissionReply{Status: "denied", Limit: 1, Observed: 1}, nil
	}}
	gate := gatetest.Manager(t, remote)
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"pending-user": 1})
	tracker.deviceGate.Store(gate)
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	done := make(chan net.Conn, 1)
	go func() {
		done <- tracker.RoutedConnection(t.Context(), server, testInboundContext("pending-user", "8.8.8.8"), nil, nil)
	}()
	<-started
	_, alive, count := tracker.GetUserTraffic()
	if count != 0 || len(alive) != 0 {
		t.Fatal("等待跨节点准入的请求被提前计为在线")
	}
	close(finish)
	_ = (<-done).Close()
	tracker.users[1].mu.RLock()
	pending := tracker.users[1].pendingConns
	tracker.users[1].mu.RUnlock()
	if pending != 0 || len(gate.Snapshot().Sources) != 0 {
		t.Fatal("拒绝后没有归还并发预留名额")
	}
}
