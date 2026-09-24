package deviceip

import (
	"net/netip"
	"slices"
	"strings"
	"sync/atomic"
)

// excludeList 是面板下发的“不计入设备数”来源名单，整体替换、只读共享。
type excludeList struct {
	exact    map[netip.Addr]struct{}
	prefixes []netip.Prefix
	source   []string
}

var excluded atomic.Pointer[excludeList]

// SetExcluded 用面板下发的名单整体替换本机名单，返回被跳过的无效项。
// 名单对本进程内所有节点生效；传空表示清空。内容未变化时返回 changed=false。
func SetExcluded(entries []string) (invalid []string, changed bool) {
	next := &excludeList{exact: make(map[netip.Addr]struct{})}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			prefix, err := netip.ParsePrefix(entry)
			if err != nil {
				invalid = append(invalid, entry)
				continue
			}
			addr := prefix.Addr()
			bits := prefix.Bits()
			if addr.Is4In6() && bits >= 96 {
				addr, bits = addr.Unmap(), bits-96
			}
			prefix = netip.PrefixFrom(addr, bits).Masked()
			if prefix.Bits() == addr.BitLen() {
				next.exact[prefix.Addr()] = struct{}{}
			} else if !slices.Contains(next.prefixes, prefix) {
				next.prefixes = append(next.prefixes, prefix)
			}
		} else {
			addr, err := netip.ParseAddr(entry)
			if err != nil || addr.Zone() != "" {
				invalid = append(invalid, entry)
				continue
			}
			next.exact[addr.Unmap()] = struct{}{}
		}
	}
	for addr := range next.exact {
		next.source = append(next.source, addr.String())
	}
	for _, prefix := range next.prefixes {
		next.source = append(next.source, prefix.String())
	}
	slices.Sort(next.source)

	var currentSource []string
	if current := excluded.Load(); current != nil {
		currentSource = current.source
	}
	if slices.Equal(currentSource, next.source) {
		return invalid, false
	}
	if len(next.source) == 0 {
		excluded.Store(nil)
	} else {
		excluded.Store(next)
	}
	return invalid, true
}

// Excluded 判断地址是否在名单内；地址应已去掉 IPv4 映射前缀。
func Excluded(addr netip.Addr) bool {
	list := excluded.Load()
	if list == nil {
		return false
	}
	if _, ok := list.exact[addr]; ok {
		return true
	}
	for _, prefix := range list.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// CountKey 返回参与设备计数的规范化地址：公网且不在名单内；否则返回空字符串。
func CountKey(raw string) string {
	normalized := Normalize(raw)
	if normalized == "" {
		return ""
	}
	if Excluded(netip.MustParseAddr(normalized)) {
		return ""
	}
	return normalized
}

// Counts 判断来源地址是否占用设备名额。
func Counts(raw string) bool { return CountKey(raw) != "" }
