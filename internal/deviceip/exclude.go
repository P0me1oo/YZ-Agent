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

// Filter 为单个节点保存设备排除名单，避免多实例互相覆盖。
type Filter struct{ excluded atomic.Pointer[excludeList] }

var defaultFilter Filter

// DefaultFilter 仅供旧的独立内核调用和测试使用。
func DefaultFilter() *Filter { return &defaultFilter }

// SetExcluded 用面板下发的名单整体替换本机名单，返回被跳过的无效项。
// 兼容旧调用；节点运行时应使用各自的 Filter。
func SetExcluded(entries []string) (invalid []string, changed bool) {
	return defaultFilter.SetExcluded(entries)
}

func (f *Filter) SetExcluded(entries []string) (invalid []string, changed bool) {
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
	if current := f.excluded.Load(); current != nil {
		currentSource = current.source
	}
	if slices.Equal(currentSource, next.source) {
		return invalid, false
	}
	if len(next.source) == 0 {
		f.excluded.Store(nil)
	} else {
		f.excluded.Store(next)
	}
	return invalid, true
}

// Prefixes 返回当前名单的全部条目，单个地址表示为完整长度的网段。
// 名单同时是可信前置服务器名单，供内核判断哪些来源可以附带真实用户地址。
func Prefixes() []netip.Prefix {
	return defaultFilter.Prefixes()
}

func (f *Filter) Prefixes() []netip.Prefix {
	list := f.excluded.Load()
	if list == nil {
		return nil
	}
	prefixes := make([]netip.Prefix, 0, len(list.exact)+len(list.prefixes))
	for addr := range list.exact {
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return append(prefixes, list.prefixes...)
}

// Excluded 判断地址是否在名单内；地址应已去掉 IPv4 映射前缀。
func Excluded(addr netip.Addr) bool {
	return defaultFilter.Excluded(addr)
}

func (f *Filter) Excluded(addr netip.Addr) bool {
	list := f.excluded.Load()
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

// CountKey 返回参与设备计数的标识：公网且不在名单内的来源，IPv6 合并到所在 /64 网段；
// 否则返回空字符串。名单按原始地址判断，精确到单个 IPv6 地址的条目同样有效。
func CountKey(raw string) string {
	return defaultFilter.CountKey(raw)
}

func (f *Filter) CountKey(raw string) string {
	addr, ok := publicAddr(raw)
	if !ok || f.Excluded(addr) {
		return ""
	}
	return deviceKey(addr)
}

// Counts 判断来源地址是否占用设备名额。
func Counts(raw string) bool { return CountKey(raw) != "" }
