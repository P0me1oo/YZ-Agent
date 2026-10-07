package singbox

import (
	"context"
	"net"

	"github.com/sagernet/sing-box/adapter"
	smeta "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 身份、流量和限速由 Node 的 WG 入口处理，这里只复用当前核心的路由与出站。
func (s *SingBox) WireGuardTCP(ctx context.Context, c net.Conn, source, destination smeta.Socksaddr, done N.CloseHandlerFunc) {
	s.mu.RLock()
	instance := s.box
	s.mu.RUnlock()
	if instance == nil {
		done(net.ErrClosed)
		return
	}
	instance.Router().RouteConnectionEx(ctx, c, adapter.InboundContext{Inbound: "wireguard-in", InboundType: "wireguard", Source: source, Destination: destination}, done)
}
func (s *SingBox) WireGuardUDP(ctx context.Context, c N.PacketConn, source, destination smeta.Socksaddr, done N.CloseHandlerFunc) {
	s.mu.RLock()
	instance := s.box
	s.mu.RUnlock()
	if instance == nil {
		done(net.ErrClosed)
		return
	}
	instance.Router().RoutePacketConnectionEx(ctx, c, adapter.InboundContext{Inbound: "wireguard-in", InboundType: "wireguard", Source: source, Destination: destination}, done)
}
