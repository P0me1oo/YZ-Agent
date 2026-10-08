package xray

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
)

type handoverClosingWriter struct {
	buf.Writer
	started chan struct{}
	finish  chan struct{}
	once    sync.Once
}

func (w *handoverClosingWriter) Close() error {
	w.once.Do(func() { close(w.started) })
	<-w.finish
	return nil
}

func (w *handoverClosingWriter) Interrupt() { _ = w.Close() }

func TestDeviceHandoverXrayWaitsForUnderlyingWriterToClose(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "Close", true: "Interrupt"}[interrupt], func(t *testing.T) {
			gate := gatetest.Manager(t, &gatetest.Remote{})
			ld := newTestDispatcher()
			ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
			ld.deviceGate.Store(gate)
			permit, err := ld.acquireDevice(t.Context(), userEmail(1), "8.8.8.8")
			if err != nil {
				t.Fatal(err)
			}
			base := &handoverClosingWriter{Writer: buf.Discard, started: make(chan struct{}), finish: make(chan struct{})}
			link := &transport.Link{Reader: nopReader{}, Writer: base}
			ld.trackLink(link, userEmail(1), "8.8.8.8", 0, true, nil, relayTracking{cancel: func() {}, devicePermit: permit})
			done := make(chan struct{})
			var finishOnce sync.Once
			finish := func() { finishOnce.Do(func() { close(base.finish) }) }
			t.Cleanup(func() { finish(); <-done })
			go func() {
				writer := link.Writer.(*closeTrackingWriter)
				if interrupt {
					writer.Interrupt()
				} else {
					_ = writer.Close()
				}
				close(done)
			}()
			<-base.started
			if len(gate.Snapshot().Sources) != 1 {
				t.Fatal("底层连接尚未关闭，不能提前归还设备名额")
			}
			finish()
			<-done
			if len(gate.Snapshot().Sources) != 0 {
				t.Fatal("底层关闭后未归还设备名额")
			}
		})
	}
}

func TestDeviceHandoverClosesXrayTransportAndPreservesOtherAccount(t *testing.T) {
	for _, useLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "Dispatch", true: "DispatchLink"}[useLink], func(t *testing.T) {
			gate := gatetest.Manager(t, &gatetest.Remote{})
			ld := newTestDispatcher()
			ld.innerDisp = &admissionDispatcher{}
			ld.UpdateLimits(map[string]int{userEmail(1): 1, userEmail(2): 2}, nil, nil)
			ld.deviceGate.Store(gate)
			connect := func(user int) (net.Conn, net.Conn) {
				server, peer := net.Pipe()
				t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
				ctx := session.ContextWithInbound(t.Context(), &session.Inbound{
					User: &protocol.MemoryUser{Email: userEmail(user)}, Conn: server,
					Source: xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 12345),
				})
				link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
				destination := xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 443)
				var err error
				if useLink {
					err = ld.DispatchLink(ctx, destination, link)
				} else {
					link, err = ld.Dispatch(ctx, destination)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = link.Writer.(*closeTrackingWriter).Close() })
				return server, peer
			}
			_, oldPeer := connect(1)
			_, oldPeer2 := connect(1)
			otherServer, otherPeer := connect(2)
			source := gatetest.Source(t, gate, 1, "8.8.8.8")
			gate.ApplyRevocations([]devicegate.Revocation{{UserID: 1, IP: source.IP, Lease: source.Lease}})
			for _, peer := range []net.Conn{oldPeer, oldPeer2} {
				_ = peer.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatalf("旧 TCP 连接未真正关闭：%v", err)
				}
			}
			done := make(chan error, 1)
			go func() { _, err := otherPeer.Write([]byte{1}); done <- err }()
			_ = otherServer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := otherServer.Read(make([]byte, 1)); err != nil {
				t.Fatal("误关了其它账号的 TCP 连接")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			alive, count := ld.GetConnectionState()
			if count != 1 || len(alive[1]) != 0 || len(alive[2]) != 1 || len(gate.Snapshot().Sources) != 1 {
				t.Fatal("实际关闭后来源或连接数没有准确回收")
			}
		})
	}
}

func TestDeviceHandoverXrayRejectedOrFailedDispatchLeavesNoClaim(t *testing.T) {
	for _, reject := range []bool{false, true} {
		remote := &gatetest.Remote{}
		if reject {
			remote.Admit = func(context.Context, devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
				return devicegate.AdmissionReply{Status: "denied", Limit: 1, Observed: 1, Reason: "cooldown"}, nil
			}
		}
		gate := gatetest.Manager(t, remote)
		ld := newTestDispatcher()
		ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
		ld.deviceGate.Store(gate)
		ld.innerDisp = &admissionDispatcher{fail: true}
		ctx := session.ContextWithInbound(t.Context(), &session.Inbound{
			User: &protocol.MemoryUser{Email: userEmail(1)}, Source: xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 1),
		})
		if _, err := ld.Dispatch(ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 1)); err == nil {
			t.Fatal("应返回准入或调度失败")
		}
		alive, count := ld.GetConnectionState()
		if count != 0 || len(alive) != 0 || len(gate.Snapshot().Sources) != 0 || len(gate.Snapshot().Pending) != 0 {
			t.Fatal("失败请求遗留设备来源")
		}
	}
}

func TestDeviceHandoverXrayFailedDispatchLinkClosesTransportBeforeRelease(t *testing.T) {
	gate := gatetest.Manager(t, &gatetest.Remote{})
	ld := newTestDispatcher()
	ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	ld.deviceGate.Store(gate)
	ld.innerDisp = &admissionDispatcher{fail: true}
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	ctx := session.ContextWithInbound(t.Context(), &session.Inbound{
		User: &protocol.MemoryUser{Email: userEmail(1)}, Conn: server,
		Source: xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 12345),
	})
	link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
	if err := ld.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 443), link); err == nil {
		t.Fatal("应返回底层调度失败")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("调度失败后底层连接仍未关闭：%v", err)
	}
	if len(gate.Snapshot().Sources) != 0 {
		t.Fatal("实际关闭后仍占用设备名额")
	}
}
