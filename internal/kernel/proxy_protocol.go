package kernel

import "net/netip"

// ProxyProtocolPrefixes 将节点开关转换为核心已有的实例级信任策略。
// 未设置开关时保留旧名单；明确关闭不解析，开启则允许所有 IPv4/IPv6 来源。
func ProxyProtocolPrefixes(setting *bool, legacy []netip.Prefix) []netip.Prefix {
	if setting == nil {
		return legacy
	}
	if !*setting {
		return nil
	}
	return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
}
