package singbox

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	singM "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 每个实例独立注册，只有旧式单地址绑定需要额外约束；原生双栈出站保持上游行为。
func directOutboundContext(ctx context.Context, custom []model.OutboundConfig) (context.Context, error) {
	bindings := make(map[string]netip.Addr)
	for i, entry := range custom {
		if !model.IsDirectOutbound(entry.Protocol) {
			continue
		}
		source, err := legacyDirectSource(entry.Settings)
		if err != nil {
			return nil, fmt.Errorf("custom_outbounds[%d]: %w", i, err)
		}
		if source.IsValid() {
			bindings[entry.Tag] = source
		}
	}
	registry := include.OutboundRegistry()
	if len(bindings) > 0 {
		outbound.Register[option.DirectOutboundOptions](registry, C.TypeDirect,
			func(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.DirectOutboundOptions) (adapter.Outbound, error) {
				base, err := direct.NewOutbound(ctx, router, logger, tag, options)
				if err != nil {
					return nil, err
				}
				source, restricted := bindings[tag]
				if !restricted {
					return base, nil
				}
				return &sourceBoundDirect{Outbound: base.(*direct.Outbound), source: source}, nil
			})
	}
	return box.Context(ctx, include.InboundRegistry(), registry, include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry()), nil
}

// sing-box 只设置 inet4_bind_address 时，IPv6 拨号仍能走未绑定的默认出口。
// 这里保留 Xray sendThrough 的单一源地址约束，也覆盖路由已解析地址的快速路径。
type sourceBoundDirect struct {
	*direct.Outbound
	source netip.Addr
}

func (s *sourceBoundDirect) checkDestination(destination singM.Socksaddr) error {
	if destination.Addr.IsValid() && destination.Addr.Unmap().Is4() != s.source.Is4() {
		return fmt.Errorf("destination address family is incompatible with send_through")
	}
	return nil
}

func (s *sourceBoundDirect) filterAddresses(destination singM.Socksaddr, addresses []netip.Addr) ([]netip.Addr, error) {
	if err := s.checkDestination(destination); err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return addresses, nil
	}
	filtered := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		if address.IsValid() && address.Unmap().Is4() == s.source.Is4() {
			filtered = append(filtered, address.Unmap())
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("resolved addresses are incompatible with send_through")
	}
	return filtered, nil
}

func (s *sourceBoundDirect) DialContext(ctx context.Context, network string, destination singM.Socksaddr) (net.Conn, error) {
	if err := s.checkDestination(destination); err != nil {
		return nil, err
	}
	// 映射地址须按实际 IPv4 地址拨号，否则上游会选到未绑定的 IPv6 拨号器。
	return s.Outbound.DialContext(ctx, network, destination.Unwrap())
}

func (s *sourceBoundDirect) ListenPacket(ctx context.Context, destination singM.Socksaddr) (net.PacketConn, error) {
	if err := s.checkDestination(destination); err != nil {
		return nil, err
	}
	conn, err := s.Outbound.ListenPacket(ctx, destination.Unwrap())
	return s.wrapPacketConn(conn, err)
}

func (s *sourceBoundDirect) DialParallel(ctx context.Context, network string, destination singM.Socksaddr, addresses []netip.Addr) (net.Conn, error) {
	filtered, err := s.filterAddresses(destination, addresses)
	if err != nil {
		return nil, err
	}
	return s.Outbound.DialParallel(ctx, network, destination.Unwrap(), filtered)
}

func (s *sourceBoundDirect) DialParallelNetwork(ctx context.Context, network string, destination singM.Socksaddr, addresses []netip.Addr,
	strategy *C.NetworkStrategy, networkType, fallbackNetworkType []C.InterfaceType, fallbackDelay time.Duration,
) (net.Conn, error) {
	filtered, err := s.filterAddresses(destination, addresses)
	if err != nil {
		return nil, err
	}
	return s.Outbound.DialParallelNetwork(ctx, network, destination.Unwrap(), filtered, strategy, networkType, fallbackNetworkType, fallbackDelay)
}

func (s *sourceBoundDirect) ListenSerialNetworkPacket(ctx context.Context, destination singM.Socksaddr, addresses []netip.Addr,
	strategy *C.NetworkStrategy, networkType, fallbackNetworkType []C.InterfaceType, fallbackDelay time.Duration,
) (net.PacketConn, netip.Addr, error) {
	filtered, err := s.filterAddresses(destination, addresses)
	if err != nil {
		return nil, netip.Addr{}, err
	}
	conn, address, err := s.Outbound.ListenSerialNetworkPacket(ctx, destination.Unwrap(), filtered, strategy, networkType, fallbackNetworkType, fallbackDelay)
	conn, err = s.wrapPacketConn(conn, err)
	return conn, address, err
}

func (s *sourceBoundDirect) wrapPacketConn(conn net.PacketConn, err error) (net.PacketConn, error) {
	if err != nil {
		return nil, err
	}
	return &sourceBoundPacketConn{NetPacketConn: bufio.NewPacketConn(conn), source: s}, nil
}

type sourceBoundPacketConn struct {
	N.NetPacketConn
	source *sourceBoundDirect
}

func (c *sourceBoundPacketConn) WriteTo(payload []byte, destination net.Addr) (int, error) {
	if err := c.source.checkDestination(singM.SocksaddrFromNet(destination)); err != nil {
		return 0, err
	}
	return c.NetPacketConn.WriteTo(payload, destination)
}

// 保留原生包接口中的域名与目标地址，不能经 net.UDPAddr 丢失域名信息。
func (c *sourceBoundPacketConn) WritePacket(buffer *buf.Buffer, destination singM.Socksaddr) error {
	if err := c.source.checkDestination(destination); err != nil {
		buffer.Release()
		return err
	}
	return c.NetPacketConn.WritePacket(buffer, destination.Unwrap())
}
