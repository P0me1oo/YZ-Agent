package xray

import (
	"context"
	"net"

	sbuf "github.com/sagernet/sing/common/buf"
	smeta "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	udpPacket "github.com/xtls/xray-core/common/protocol/udp"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet/udp"
)

func (x *Xray) wireGuardContext(parent context.Context, source smeta.Socksaddr) (context.Context, context.CancelFunc, *LimitDispatcher) {
	x.mu.Lock()
	d := x.limitDispatcher
	x.mu.Unlock()
	if d == nil {
		return nil, func() {}, nil
	}
	ctx, cancel := context.WithCancel(d.coreContext)
	stop := context.AfterFunc(parent, cancel)
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Name: "wireguard", Tag: "wireguard-in", Source: xnet.TCPDestination(xnet.ParseAddress(source.AddrString()), xnet.Port(source.Port))})
	ctx = session.ContextWithContent(ctx, &session.Content{})
	return ctx, func() { stop(); cancel() }, d
}

func (x *Xray) WireGuardTCP(parent context.Context, c net.Conn, source, destination smeta.Socksaddr, done N.CloseHandlerFunc) {
	ctx, cancel, d := x.wireGuardContext(parent, source)
	defer cancel()
	if d == nil {
		done(net.ErrClosed)
		return
	}
	err := d.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress(destination.AddrString()), xnet.Port(destination.Port)), &transport.Link{Reader: buf.NewReader(c), Writer: buf.NewWriter(c)})
	done(err)
}
func (x *Xray) WireGuardUDP(parent context.Context, c N.PacketConn, source, destination smeta.Socksaddr, done N.CloseHandlerFunc) {
	ctx, cancel, d := x.wireGuardContext(parent, source)
	defer cancel()
	defer done(nil)
	if d == nil {
		return
	}
	session.InboundFromContext(ctx).Source.Network = xnet.Network_UDP
	dispatcher := udp.NewDispatcher(d, func(_ context.Context, p *udpPacket.Packet) {
		defer p.Payload.Release()
		response := sbuf.NewSize(int(p.Payload.Len()))
		_, _ = response.Write(p.Payload.Bytes())
		_ = c.WritePacket(response, smeta.ParseSocksaddr(p.Source.NetAddr()))
	})
	defer dispatcher.RemoveRay()
	packet := sbuf.NewPacket()
	defer packet.Release()
	for {
		packet.Reset()
		dest, err := c.ReadPacket(packet)
		if err != nil {
			return
		}
		target := xnet.UDPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port))
		payload := buf.FromBytes(append([]byte(nil), packet.Bytes()...))
		payload.UDP = &target
		dispatcher.Dispatch(ctx, target, payload)
	}
}
