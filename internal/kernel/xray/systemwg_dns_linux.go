//go:build linux

package xray

import (
	"context"
	"errors"
	"reflect"

	"github.com/P0me1oo/YZ-Agent/internal/systemwg"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/dns"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/freedom"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
)

// The pinned Xray transport stores DNS in a process-global variable. A machine
// runs multiple cores; resolve after routing, using this landing's own feature,
// before freedom can consult another instance's global resolver. Keeping this
// at the outbound boundary preserves domain routing and the direct IP guard.
func init() {
	typ := reflect.TypeOf((*freedom.Config)(nil))
	original := typeCreatorRegistry[typ]
	typeCreatorRegistry[typ] = func(ctx context.Context, cfg interface{}) (interface{}, error) {
		outbound, err := original(ctx, cfg)
		if err != nil {
			return nil, err
		}
		runtime, _ := ctx.Value(systemWGKey{}).(*systemwg.Runtime)
		if runtime == nil || runtime.XrayLandingNamespace() == "" {
			return outbound, nil
		}
		wrapped := &systemWGDirect{Outbound: outbound.(proxy.Outbound)}
		if stream, ok := session.StreamSettingsFromContext(ctx).(*internet.MemoryStreamConfig); ok && stream.SocketSettings != nil {
			wrapped.strategy = stream.SocketSettings.DomainStrategy
		}
		err = core.RequireFeatures(ctx, func(client dns.Client) { wrapped.dns = client })
		return wrapped, err
	}
}

type systemWGDirect struct {
	proxy.Outbound
	dns      dns.Client
	strategy internet.DomainStrategy
}

func (o *systemWGDirect) resolve(ctx context.Context, target xnet.Destination) (xnet.Destination, error) {
	if err := ctx.Err(); err != nil {
		return target, err
	}
	if target.Address == nil {
		return target, errors.New("WG 落地目标为空")
	}
	if !target.Address.Family().IsDomain() {
		return target, nil
	}
	if o.dns == nil {
		return target, errors.New("WG 落地 DNS 未初始化")
	}
	strategy := o.strategy
	if !strategy.HasStrategy() {
		strategy = internet.DomainStrategy_USE_IP
	}
	ips, _, err := o.dns.LookupIP(target.Address.Domain(), dns.IPOption{IPv4Enable: strategy.PreferIP4(), IPv6Enable: strategy.PreferIP6()})
	if (err != nil || len(ips) == 0) && strategy.HasFallback() {
		ips, _, err = o.dns.LookupIP(target.Address.Domain(), dns.IPOption{IPv4Enable: strategy.FallbackIP4(), IPv6Enable: strategy.FallbackIP6()})
	}
	if err != nil {
		return target, err
	}
	if len(ips) == 0 {
		return target, dns.ErrEmptyResponse
	}
	target.Address = xnet.IPAddress(ips[0])
	return target, nil
}

func (o *systemWGDirect) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	outbounds := session.OutboundsFromContext(ctx)
	if len(outbounds) == 0 {
		return errors.New("WG 落地缺少出站上下文")
	}
	current := outbounds[len(outbounds)-1]
	target, err := o.resolve(ctx, current.Target)
	if err != nil {
		return err
	}
	if current.OriginalTarget.Address == nil {
		current.OriginalTarget = current.Target
	}
	current.Target = target
	copied := *link
	if target.Network == xnet.Network_UDP {
		copied.Reader = &systemWGPacketReader{Reader: link.Reader, ctx: ctx, outbound: o}
	}
	return o.Outbound.Process(ctx, &copied, dialer)
}

type systemWGPacketReader struct {
	buf.Reader
	ctx      context.Context
	outbound *systemWGDirect
}

func (r *systemWGPacketReader) Interrupt() { _ = common.Interrupt(r.Reader) }

func (r *systemWGPacketReader) Close() error { return common.Close(r.Reader) }

func (r *systemWGPacketReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	packets, err := r.Reader.ReadMultiBuffer()
	for _, packet := range packets {
		if packet.UDP == nil {
			continue
		}
		destination, resolveErr := r.outbound.resolve(r.ctx, *packet.UDP)
		if resolveErr != nil {
			buf.ReleaseMulti(packets)
			return nil, resolveErr
		}
		packet.UDP = &destination
	}
	return packets, err
}
