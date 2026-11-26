package firewall

import (
	"fmt"
	"net/netip"
	"slices"
)

type addressFamilyProbe interface {
	IPv6Available() (bool, error)
}

// 只在内核明确不支持 IPv6 时缩小计划；地址和默认路由缺失不能作为依据。
func (b *systemBackend) effectivePlan(plan Plan) (Plan, error) {
	probe, ok := b.commands.(addressFamilyProbe)
	if !ok {
		return plan, nil
	}
	available, err := probe.IPv6Available()
	if err != nil {
		return plan, fmt.Errorf("核对内核 IPv6 能力: %w", err)
	}
	b.ipv6Unavailable = !available
	if available {
		return plan, nil
	}
	listeners := slices.DeleteFunc(slices.Clone(plan.Listeners), func(rule Rule) bool { return rule.Family == 6 })
	if len(plan.Listeners) > 0 && len(listeners) == 0 {
		return plan, fmt.Errorf("节点要求 IPv6 监听，但当前内核不支持 IPv6")
	}
	plan.Listeners = listeners
	plan.Redirects = slices.DeleteFunc(slices.Clone(plan.Redirects), func(rule Rule) bool { return rule.Family == 6 })
	plan.Blocked = slices.DeleteFunc(slices.Clone(plan.Blocked), func(raw string) bool {
		prefix, err := netip.ParsePrefix(raw)
		return err == nil && prefix.Addr().Is6()
	})
	return plan, nil
}
