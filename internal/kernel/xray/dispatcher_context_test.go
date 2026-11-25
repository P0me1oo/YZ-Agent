package xray

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate/gatetest"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

type captureContextDispatcher struct {
	routing.Dispatcher
	ctx context.Context
}

func (d *captureContextDispatcher) DispatchLink(ctx context.Context, _ xnet.Destination, _ *transport.Link) error {
	d.ctx = ctx
	return nil
}

func TestDispatchLinkCancellationOwnedByConnection(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "revoke"}[revoke], func(t *testing.T) {
			gate := gatetest.Manager(t, &gatetest.Remote{})
			ld := newTestDispatcher()
			ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
			ld.deviceGate.Store(gate)
			inner := &captureContextDispatcher{}
			ld.innerDisp = inner
			ctx := session.ContextWithInbound(t.Context(), &session.Inbound{
				User:   &protocol.MemoryUser{Email: userEmail(1)},
				Source: xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 12345),
			})
			link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
			if err := ld.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress("1.1.1.1"), 443), link); err != nil {
				t.Fatal(err)
			}
			if inner.ctx.Err() != nil {
				t.Fatal("premature cancellation")
			}
			if revoke {
				source := gatetest.Source(t, gate, 1, "8.8.8.8")
				gate.ApplyRevocations([]devicegate.Revocation{{UserID: 1, IP: source.IP, Lease: source.Lease}})
			} else {
				_ = link.Writer.(*closeTrackingWriter).Close()
			}
			if inner.ctx.Err() != context.Canceled {
				t.Fatal("connection did not cancel its context")
			}
			if len(gate.Snapshot().Sources) != 0 {
				t.Fatal("device claim not released")
			}
		})
	}
}

func TestDispatchLinkWithoutUserDoesNotCreateUnownedCancellation(t *testing.T) {
	ld := newTestDispatcher()
	ld.SetRelayRoutes(map[xnet.Port]int{1: 1})
	inner := &captureContextDispatcher{}
	ld.innerDisp = inner
	ctx := t.Context()
	link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
	if err := ld.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress("1.1.1.1"), 443), link); err != nil {
		t.Fatal(err)
	}
	if inner.ctx.Done() != ctx.Done() {
		t.Fatal("internal dispatch created an unowned context")
	}
}
