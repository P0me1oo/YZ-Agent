package directwg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func waitWGSource(t *testing.T, state *State, key, ip string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if state.admit(key, netip.MustParseAddr(ip), true, time.Now()) {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("来源异步准入未完成")
		case <-ticker.C:
		}
	}
}

func TestDeviceHandoverWireGuardDoesNotBlockOtherUsersAndClosesSelectedSource(t *testing.T) {
	started, proceed := make(chan struct{}), make(chan struct{})
	remote := &gatetest.Remote{Admit: func(ctx context.Context, req devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
		if req.UserID == 1 {
			close(started)
			select {
			case <-ctx.Done():
				return devicegate.AdmissionReply{}, ctx.Err()
			case <-proceed:
			}
		}
		return devicegate.AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", req.UserID)}, nil
	}}
	gate := gatetest.Manager(t, remote)
	s := newState()
	s.deviceGate = gate
	first, second := stateUser(t), stateUser(t)
	second.ID, second.UUID, second.WireGuard.Address = 2, "second-handover-test", []string{"10.0.0.3/32"}
	s.replaceUsers([]model.UserSpec{first, second})
	t.Cleanup(func() { s.replaceUsers(nil) })
	if s.admit(first.WireGuard.PublicKey, netip.MustParseAddr("8.8.8.8"), true, time.Now()) {
		t.Fatal("未获准的首包不应放行")
	}
	<-started
	waitWGSource(t, s, second.WireGuard.PublicKey, "8.8.8.8")
	close(proceed)
	waitWGSource(t, s, first.WireGuard.PublicKey, "8.8.8.8")
	before := gatetest.Source(t, gate, 1, "8.8.8.8").ConnectSequence
	if !s.admit(first.WireGuard.PublicKey, netip.MustParseAddr("8.8.8.8"), false, time.Now()) {
		t.Fatal("已获准来源的保活应通过")
	}
	if gatetest.Source(t, gate, 1, "8.8.8.8").ConnectSequence != before {
		t.Fatal("保活不应冒充新连接")
	}
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	_, release, ok := s.track(1, server)
	if !ok {
		t.Fatal("已授权来源不能建立内部连接")
	}
	t.Cleanup(release)
	source := gatetest.Source(t, gate, 1, "8.8.8.8")
	if source.ConnectSequence <= before {
		t.Fatal("内部新连接未更新来源排序时间")
	}
	gate.ApplyRevocations([]devicegate.Revocation{{UserID: 1, IP: source.IP, Lease: source.Lease}})
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("旧来源的内部连接没有关闭：%v", err)
	}
	_, alive, count := s.snapshot()
	if len(alive[1]) != 0 || len(alive[2]) != 1 || count != 0 || len(gate.Snapshot().Sources) != 1 {
		t.Fatal("WG 来源关闭未隔离账号或名额未正确回收")
	}
}

func TestDeviceHandoverWireGuardUsesCapturedSourceWhenEndpointChanges(t *testing.T) {
	gate := gatetest.Manager(t, &gatetest.Remote{})
	s := newState()
	s.deviceGate = gate
	user := stateUser(t)
	user.DeviceLimit = 2
	s.replaceUsers([]model.UserSpec{user})
	t.Cleanup(func() { s.replaceUsers(nil) })
	waitWGSource(t, s, user.WireGuard.PublicKey, "8.8.8.8")
	waitWGSource(t, s, user.WireGuard.PublicKey, "1.1.1.1")
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	_, release, ok := s.track(1, server, netip.MustParseAddr("8.8.8.8"))
	if !ok {
		t.Fatal("原来源仍有效时应按捕获的来源登记")
	}
	t.Cleanup(release)
	old := gatetest.Source(t, gate, 1, "8.8.8.8")
	gate.ApplyRevocations([]devicegate.Revocation{{UserID: 1, IP: old.IP, Lease: old.Lease}})
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatal("旧来源的连接被错误归到了新来源")
	}
	_, alive, _ := s.snapshot()
	if len(alive[1]) != 1 || !alive[1]["1.1.1.1"] {
		t.Fatal("关闭原来源时误清了新来源")
	}
}
