package xray

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
)

func TestLeaseCacheExpiryClosesXrayTransport(t *testing.T) {
	gate := gatetest.Manager(t, &gatetest.Remote{CacheDuration: 300 * time.Millisecond})
	gate.SetUsers([]int{1})
	dispatcher := newTestDispatcher()
	dispatcher.innerDisp = &admissionDispatcher{}
	dispatcher.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	dispatcher.deviceGate.Store(gate)
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	ctx := session.ContextWithInbound(t.Context(), &session.Inbound{User: &protocol.MemoryUser{Email: userEmail(1)},
		Conn: server, Source: xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 12345)})
	link, err := dispatcher.Dispatch(ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 443))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = link.Writer.(*closeTrackingWriter).Close() })
	_ = peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("许可到期没有关闭 Xray 连接：%v", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(gate.Snapshot().Sources) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_, count := dispatcher.GetConnectionState()
	if count != 0 || len(gate.Snapshot().Sources) != 0 {
		t.Fatal("关闭后设备或连接计数未归还")
	}
}
