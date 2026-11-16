package singbox

import (
	"context"
	"fmt"
	"maps"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	dnsOutbound "github.com/sagernet/sing-box/protocol/dns"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

const dnsAccountingType = "yz-dns"

// DNS 接管走普通出站派发，复用核心 DNS 处理器及 Agent 的用户计数、准入和限速。
// 注册仅属于 Agent 实例，不恢复核心已移除的原生 dns 出站配置。
func registerDNSAccounting(registry *outbound.Registry) {
	outbound.Register[option.StubOptions](registry, dnsAccountingType, newDNSAccountingOutbound)
}

func newDNSAccountingOutbound(
	ctx context.Context,
	router adapter.Router,
	logger log.ContextLogger,
	tag string,
	options option.StubOptions,
) (adapter.Outbound, error) {
	base, err := dnsOutbound.NewOutbound(
		ctx,
		router,
		logger,
		tag,
		options,
	)
	if err != nil {
		return nil, err
	}
	return &dnsAccountingOutbound{
		Outbound: base.(*dnsOutbound.Outbound), router: service.FromContext[adapter.DNSRouter](ctx),
	}, nil
}

type dnsAccountingOutbound struct {
	*dnsOutbound.Outbound
	router adapter.DNSRouter
}

func (d *dnsAccountingOutbound) NewPacketConnection(
	ctx context.Context,
	conn N.PacketConn,
	metadata adapter.InboundContext,
	onClose N.CloseHandlerFunc,
) {
	// 核心的普通读取回退会再次调用原连接；在 DNS 小报文路径保留计数包装，避免重复回调。
	err := dnsOutbound.NewDNSPacketConnection(
		ctx,
		d.router,
		&dnsAccountingPacketConn{PacketConn: conn},
		nil,
		metadata,
	)
	N.CloseOnHandshakeFailure(conn, onClose, err)
}

type dnsAccountingPacketConn struct {
	N.PacketConn
}

func (c *dnsAccountingPacketConn) Upstream() any           { return c.PacketConn }
func (c *dnsAccountingPacketConn) ReaderReplaceable() bool { return false }

func prepareDNSAccounting(cfg M) {
	route, ok := cfg["route"].(M)
	if !ok {
		return
	}
	rules, _ := route["rules"].([]M)
	hasDNS := false
	for _, rule := range rules {
		if rule["action"] == "hijack-dns" {
			hasDNS = true
			break
		}
	}
	if !hasDNS {
		return
	}
	used := make(map[string]bool)
	for _, key := range []string{"outbounds", "endpoints"} {
		entries, _ := cfg[key].([]M)
		for _, entry := range entries {
			tag, _ := entry["tag"].(string)
			used[tag] = true
		}
	}
	tag := "yz-dns"
	for suffix := 1; used[tag]; suffix++ {
		tag = fmt.Sprintf("yz-dns-%d", suffix)
	}
	converted := make([]M, len(rules))
	for i, rule := range rules {
		converted[i] = rule
		if rule["action"] == "hijack-dns" {
			converted[i] = maps.Clone(rule)
			converted[i]["action"], converted[i]["outbound"] = "route", tag
		}
	}
	route["rules"] = converted
	entries, _ := cfg["outbounds"].([]M)
	cfg["outbounds"] = append(entries, M{"type": dnsAccountingType, "tag": tag})
}
