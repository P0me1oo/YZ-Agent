package deviceip

import "net/netip"

// Normalize 返回可计数的公网来源地址；非公网地址返回空字符串。
func Normalize(raw string) string {
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return ""
	}
	if addr.Is4() {
		for _, prefix := range nonPublicV4 {
			if prefix.Contains(addr) {
				return ""
			}
		}
		return addr.String()
	}
	if !netip.MustParsePrefix("2000::/3").Contains(addr) {
		return ""
	}
	for _, prefix := range nonPublicV6 {
		if prefix.Contains(addr) {
			return ""
		}
	}
	return addr.String()
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
