package firewall

import (
	"context"
	"fmt"
	"strings"
)

func (b *systemBackend) ufwManagedPosition(rule Rule, existing []ufwRule) (own, manual, blocker int, err error) {
	own, manual, blocker = -1, -1, -1
	for index, current := range existing {
		if !overlaps(rule, current.Rule) {
			continue
		}
		marked := strings.HasPrefix(current.Comment, "yz-agent:") || strings.HasPrefix(current.Comment, "yzboard-node:")
		if marked && !b.ownsUFWComment(current.Rule, current.Comment) {
			return own, manual, blocker, fmt.Errorf("UFW 端口 %s/%s 存在其他实例规则，不能接管", rule.Ports, rule.Protocol)
		}
		if current.Action != "ALLOW" && blocker < 0 {
			blocker = index
		}
		if current.ScopedSource || current.Rule != rule || current.Action != "ALLOW" {
			continue
		}
		if b.ownsUFWComment(rule, current.Comment) {
			own = index
		} else {
			manual = index
		}
	}
	return own, manual, blocker, nil
}

// 在 UFW 用户规则前放行已确认监听的节点，原手工规则保持原位；退出后自动恢复其效果。
func (b *systemBackend) ensureUFWManaged(ctx context.Context, owned ownedRule, existing []ufwRule) error {
	own, manual, blocker, err := b.ufwManagedPosition(owned.Rule, existing)
	if err != nil {
		return err
	}
	if manual >= 0 {
		if blocker >= 0 && manual > blocker {
			return fmt.Errorf("UFW 端口 %s/%s 的手工公开放行位于拒绝规则之后，无法保留其原位并接管；请关闭自动管理或调整该手工规则", owned.Rule.Ports, owned.Rule.Protocol)
		}
		return b.forget(owned)
	}
	if own >= 0 && (blocker < 0 || own < blocker) {
		if err := b.remember(owned); err != nil {
			return err
		}
		if existing[own].Comment == b.ufwComment(owned.Rule) {
			return nil
		}
		if _, err := b.commands.Run(ctx, "ufw", "", b.ufwArgs(owned.Rule, false)...); err != nil {
			return err
		}
	} else {
		if own >= 0 {
			if err := b.removeUFW(ctx, owned, existing, ""); err != nil {
				return err
			}
		}
		if err := b.remember(owned); err != nil {
			return err
		}
		// prepend 分别处理 IPv4/IPv6 的首条位置，避免按全局序号插入时混淆地址族。
		args := append([]string{"prepend"}, b.ufwArgs(owned.Rule, false)...)
		if _, err := b.commands.Run(ctx, "ufw", "", args...); err != nil {
			return fmt.Errorf("接管 UFW 节点端口失败: %w", err)
		}
	}
	output, err := b.commands.Run(ctx, "ufw", "", "status", "numbered")
	if err != nil {
		return err
	}
	verified, _, rejected, err := b.ufwManagedPosition(owned.Rule, parseUFWStatus(output))
	if err != nil || verified < 0 || (rejected >= 0 && verified > rejected) {
		return fmt.Errorf("UFW 节点端口接管核验失败")
	}
	return nil
}
