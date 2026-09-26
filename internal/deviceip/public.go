package deviceip

import "net/netip"

// Normalize 返回可计数的公网来源标识；非公网地址返回空字符串。
// IPv4 按单个地址计；IPv6 按前 64 位网段计，返回网段地址（如 2001:db8:1:2::）。
// 同一台设备会轮换临时 IPv6 地址，但通常不离开所在的 /64，按网段计可避免被算成多台。
func Normalize(raw string) string {
	addr, ok := publicAddr(raw)
	if !ok {
		return ""
	}
	return deviceKey(addr)
}

// PublicAddress 返回完整的公网来源地址，供面板在原始地址上应用排除名单。
func PublicAddress(raw string) string {
	addr, ok := publicAddr(raw)
	if !ok {
		return ""
	}
	return addr.String()
}

// publicAddr 解析来源地址，去掉 IPv4 映射前缀，只接受公网单播地址。
func publicAddr(raw string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return netip.Addr{}, false
	}
	if addr.Is4() {
		for _, prefix := range nonPublicV4 {
			if prefix.Contains(addr) {
				return netip.Addr{}, false
			}
		}
		return addr, true
	}
	if !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return netip.Addr{}, false
	}
	for _, prefix := range nonPublicV6 {
		if prefix.Contains(addr) {
			return netip.Addr{}, false
		}
	}
	return addr, true
}

// DeviceIPv6PrefixBits 是 IPv6 来源按网段合并为一台设备时使用的前缀长度。
const DeviceIPv6PrefixBits = 64

func deviceKey(addr netip.Addr) string {
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, DeviceIPv6PrefixBits).Masked().Addr().String()
}

func Public(raw string) bool { return Normalize(raw) != "" }

var nonPublicV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
}

var nonPublicV6 = []netip.Prefix{
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
}
