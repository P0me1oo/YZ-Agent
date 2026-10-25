package kernel

import (
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"net/netip"
)

func ProxyProtocolPrefixesForNode(node *model.NodeSpec) []netip.Prefix {
	if node.BalancerReceiver == nil {
		return ProxyProtocolPrefixes(node.ProxyProtocolSetting())
	}
	var prefixes []netip.Prefix
	for _, value := range node.BalancerReceiver.TrustedAddresses {
		address, err := netip.ParseAddr(value)
		if err != nil {
			continue
		}
		address = address.Unmap()
		prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
	}
	return prefixes
}

// ProxyProtocolPrefixes 将节点开关转换为核心已有的实例级信任策略。
// 关闭不解析，开启允许所有 IPv4/IPv6 来源；统计排除名单不参与接收判断。
func ProxyProtocolPrefixes(setting *bool) []netip.Prefix {
	if setting == nil || !*setting {
		return nil
	}
	return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
}
