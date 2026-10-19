package kernel

import "net/netip"

// ProxyProtocolPrefixes 将节点开关转换为核心已有的实例级信任策略。
// 关闭不解析，开启允许所有 IPv4/IPv6 来源；统计排除名单不参与接收判断。
func ProxyProtocolPrefixes(setting *bool) []netip.Prefix {
	if setting == nil || !*setting {
		return nil
	}
	return []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}
}
