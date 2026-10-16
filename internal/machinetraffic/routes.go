package machinetraffic

import "github.com/vishvananda/netlink"

func isDefaultRoute(r netlink.Route) bool {
	if r.Type != 1 || r.Table != 254 {
		return false
	}
	if r.Dst == nil {
		return true
	}
	// netlink 会将省略的默认目标补成 0.0.0.0/0 或 ::/0，不能只判断 nil。
	prefix, bits := r.Dst.Mask.Size()
	return prefix == 0 && (bits == 32 || bits == 128) && r.Dst.IP.IsUnspecified()
}

// 每个地址族分别选择主路由表中优先级最高的默认出口。
func defaultRouteLinkIndices(routes []netlink.Route) []int {
	best := int(^uint(0) >> 1)
	for _, r := range routes {
		if isDefaultRoute(r) && r.Priority < best {
			best = r.Priority
		}
	}
	var indices []int
	for _, r := range routes {
		if !isDefaultRoute(r) || r.Priority != best {
			continue
		}
		if r.LinkIndex > 0 {
			indices = append(indices, r.LinkIndex)
		}
		for _, hop := range r.MultiPath {
			if hop.LinkIndex > 0 {
				indices = append(indices, hop.LinkIndex)
			}
		}
	}
	return indices
}
