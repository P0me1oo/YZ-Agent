package firewall

import (
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

// 手工拒绝和限流接管其覆盖的端口；只为剩余范围创建自动放行。
func splitUFWManagedAllows(allows []Rule, existing []ufwRule) (managed, manual []Rule) {
	for _, rule := range allows {
		var exclusions []portset.Range
		for _, current := range existing {
			if current.Action != "ALLOW" && overlaps(rule, current.Rule) {
				exclusions = append(exclusions, portset.Range{
					From: max(rule.Ports.From, current.Rule.Ports.From),
					To:   min(rule.Ports.To, current.Rule.Ports.To),
				})
			}
		}
		from := rule.Ports.From
		for _, excluded := range portset.Merge(exclusions) {
			if from < excluded.From {
				part := rule
				part.Ports = portset.Range{From: from, To: excluded.From - 1}
				managed = append(managed, part)
			}
			part := rule
			part.Ports = excluded
			manual = append(manual, part)
			from = excluded.To + 1
		}
		if from <= rule.Ports.To {
			rule.Ports.From = from
			managed = append(managed, rule)
		}
	}
	return managed, manual
}

func rulesOverlapAny(rule Rule, others []Rule) bool {
	for _, other := range others {
		if overlaps(rule, other) {
			return true
		}
	}
	return false
}

// 端口转发会改写防火墙匹配的目标端口，无法按固定监听的方式直接跳过。
func checkUFWRedirectRestrictions(redirects []Rule, existing []ufwRule) error {
	for _, redirect := range redirects {
		external := redirect
		external.Target = 0
		target := external
		target.Ports = portset.Range{From: redirect.Target, To: redirect.Target}
		if err := checkUFWBlockingRules([]Rule{external, target}, existing); err != nil {
			return err
		}
	}
	return nil
}

func (b *systemBackend) reportUFWManualRestrictions(manual []Rule) {
	next := make(map[string]bool, len(manual))
	for _, rule := range manual {
		key := rule.key()
		next[key] = true
		if !b.manualUFW[key] {
			nlog.Core().Info("UFW 端口已有手工限制，跳过自动放行，按现有规则提供服务", "port", rule.Ports.String(), "protocol", rule.Protocol, "family", rule.Family)
		}
	}
	b.manualUFW = next
}
