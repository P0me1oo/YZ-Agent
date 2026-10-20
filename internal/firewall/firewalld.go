package firewall

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

func (b *systemBackend) richRule(rule Rule) string {
	// 使用稳定的独立优先级区分普通放行规则；归属仍以持久记录为准。
	sum := sha256.Sum256([]byte(b.scope))
	priority := 20000 + binary.BigEndian.Uint32(sum[:4])%10000
	return richRuleAtPriority(rule, int(priority))
}

func (b *systemBackend) ownedRichRule(owned ownedRule) string {
	if owned.Priority == 0 {
		return b.richRule(owned.Rule)
	}
	return richRuleAtPriority(owned.Rule, owned.Priority)
}

func richRuleAtPriority(rule Rule, priority int) string {
	text := fmt.Sprintf(`rule priority="%d" family="ipv%d"`, priority, rule.Family)
	if rule.Source != "" {
		text += fmt.Sprintf(` source address="%s"`, rule.Source)
	}
	if rule.Address != "" {
		text += fmt.Sprintf(` destination address="%s"`, rule.Address)
	}
	return text + fmt.Sprintf(` port port="%s" protocol="%s" accept`, rule.Ports, rule.Protocol)
}

func (b *systemBackend) firewalldRules(ctx context.Context, zone string) (map[string]bool, error) {
	output, err := b.commands.Run(ctx, "firewall-cmd", "", "--zone="+zone, "--list-rich-rules")
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result[line] = true
		}
	}
	return result, nil
}

func (b *systemBackend) ensureFirewalld(ctx context.Context, owned ownedRule, existing map[string]bool) error {
	rule := b.ownedRichRule(owned)
	if owned.Priority != 0 {
		for text := range existing {
			if strings.Contains(text, `priority="-32768"`) &&
				(strings.Contains(text, " drop") || strings.Contains(text, " reject") || strings.Contains(text, " limit")) &&
				richRuleMayOverlap(text, owned.Rule) {
				return fmt.Errorf("firewalld 端口已有最高优先级限制，无法安全接管；请关闭自动管理或调整该规则")
			}
		}
	}
	if existing[rule] {
		if owned.Priority != 0 && !b.hasOwned(owned) {
			return fmt.Errorf("firewalld 已有同优先级端口规则，无法确认归属；请关闭自动管理或调整共用端口")
		}
		// 已存在但没有归属记录的规则不认领，避免删除管理员或其他进程的配置。
		return nil
	}
	if err := b.remember(owned); err != nil {
		return err
	}
	output, err := b.commands.Run(ctx, "firewall-cmd", "", "--zone="+owned.Zone, "--add-rich-rule="+rule)
	if err != nil {
		return fmt.Errorf("添加 firewalld 放行规则: %w", err)
	}
	if strings.Contains(output, "ALREADY_ENABLED") {
		if err := b.forget(owned); err != nil {
			return err
		}
		if owned.Priority != 0 {
			return fmt.Errorf("firewalld 端口规则在创建时被其他操作抢先写入，无法确认归属")
		}
		return nil
	}
	if owned.Priority != 0 {
		actual, err := b.firewalldRules(ctx, owned.Zone)
		if err != nil {
			return err
		}
		if !actual[rule] {
			return fmt.Errorf("firewalld 端口接管核验失败")
		}
	}
	existing[rule] = true
	return nil
}
