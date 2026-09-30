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
	rule := b.richRule(owned.Rule)
	if existing[rule] {
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
		return b.forget(owned)
	}
	existing[rule] = true
	return nil
}
