package xray

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"reflect"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type protectedDirectKey struct{}

func init() {
	typ := reflect.TypeOf((*freedom.Config)(nil))
	original := typeCreatorRegistry[typ]
	typeCreatorRegistry[typ] = func(ctx context.Context, cfg interface{}) (interface{}, error) {
		outbound, err := original(ctx, cfg)
		if err != nil {
			return nil, err
		}
		protected, _ := ctx.Value(protectedDirectKey{}).(map[string]bool)
		handler := session.FullHandlerFromContext(ctx)
		if handler == nil || !protected[handler.Tag()] {
			return outbound, nil
		}
		var strategy internet.DomainStrategy
		if stream, ok := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig); ok && stream.SocketSettings != nil {
			strategy = stream.SocketSettings.DomainStrategy
		}
		return &protectedDirect{Outbound: outbound.(proxy.Outbound), strategy: strategy}, nil
	}
}

type protectedDirect struct {
	proxy.Outbound
	strategy internet.DomainStrategy
}

func (o *protectedDirect) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	return o.Outbound.Process(ctx, link, &protectedDirectDialer{Dialer: dialer, strategy: o.strategy})
}

type protectedDirectDialer struct {
	internet.Dialer
	strategy internet.DomainStrategy
}

var protectedPrefixes = func() []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(privateDestinationCIDRs))
	for _, value := range privateDestinationCIDRs {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}()

func publicDestination(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range protectedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func (d *protectedDirectDialer) Dial(ctx context.Context, target xnet.Destination) (stat.Connection, error) {
	if target.Address == nil {
		return nil, errors.New("直连目标为空")
	}
	if target.Address.Family().IsIP() {
		if !publicDestination(target.Address.IP()) {
			return nil, errors.New("直连目标不在公网地址范围")
		}
		return d.Dialer.Dial(ctx, target)
	}
	var ips []net.IP
	if d.strategy.HasStrategy() {
		var gateway xnet.Address
		if outbounds := session.OutboundsFromContext(ctx); len(outbounds) > 0 {
			gateway = outbounds[len(outbounds)-1].Gateway
		}
		var err error
		ips, err = internet.LookupForIP(target.Address.Domain(), d.strategy, gateway)
		if err != nil && d.strategy.ForceIP() {
			return nil, err
		}
	}
	if len(ips) == 0 {
		var err error
		ips, err = net.DefaultResolver.LookupIP(ctx, "ip", target.Address.Domain())
		if err != nil {
			return nil, err
		}
	}
	var last error
	for _, ip := range ips {
		if !publicDestination(ip) {
			continue
		}
		target.Address = xnet.IPAddress(ip)
		conn, err := d.Dialer.Dial(ctx, target)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last != nil {
		return nil, last
	}
	return nil, errors.New("直连域名没有可用的公网地址")
}
