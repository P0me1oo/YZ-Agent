package firewall

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (b *systemBackend) Apply(ctx context.Context, allows, redirects []Rule) error {
	for _, rule := range append(append([]Rule(nil), allows...), redirects...) {
		if !rule.valid() {
			return fmt.Errorf("拒绝无效的防火墙规则")
		}
	}
	if b.state.Namespace == "" && !b.recovering && len(b.protected) == 0 && !hasSources(allows) {
		// 旧记录必须先按原标记清理成功；失败时保持旧标记，重启后继续恢复。
		if err := b.apply(ctx, nil, nil); err != nil {
			return err
		}
		b.state.Namespace = "yz-agent"
		if err := b.save(); err != nil {
			b.state.Namespace = ""
			return err
		}
	}
	return b.apply(ctx, allows, redirects)
}

func (b *systemBackend) apply(ctx context.Context, allows, redirects []Rule) error {
	if !b.cfg.IsEnabled() {
		allows, redirects = nil, nil
		if len(b.state.Owned) == 0 && b.state.Redirect == "" {
			return nil
		}
	}
	// 旧版可能把相邻端口合成一条规则；保留其中的待确认端口时，不能声称其余端口已收紧。
	for _, owned := range b.state.Owned {
		if !b.keepOwned(owned.Rule) {
			continue
		}
		for _, wanted := range allows {
			// 仅有 IPv4 来源时仍须撤销旧 IPv6 全开放规则，不能漏掉另一地址族。
			samePort := owned.Rule.Protocol == wanted.Protocol && owned.Rule.Ports.Overlaps(wanted.Ports)
			if wanted.Source != "" && samePort &&
				(owned.Rule.Family != wanted.Family || overlaps(owned.Rule, wanted)) &&
				!retainedSourceAllowed(owned.Rule, wanted, allows) {
				return pending("旧托管端口范围尚有未完成检查的节点，请确认同范围端口；原规则暂时保留")
			}
		}
	}
	ufwWanted := b.cfg.Backend == "" || b.cfg.Backend == "auto" || b.cfg.Backend == "ufw"
	fdWanted := b.cfg.Backend == "" || b.cfg.Backend == "auto" || b.cfg.Backend == "firewalld"
	var ufwActive, fdActive bool
	var ufwRules []ufwRule
	var ufwAdded string
	var err error
	if ufwWanted || b.ownsBackend("ufw") {
		if b.commands.Available("ufw") {
			var status string
			status, err = b.commands.Run(ctx, "ufw", "", "status", "numbered")
			if err != nil {
				return fmt.Errorf("检查 UFW 状态: %w", err)
			}
			switch {
			case strings.Contains(status, "Status: active"):
				ufwActive, ufwRules = true, parseUFWStatus(status)
			case strings.Contains(status, "Status: inactive"):
				if b.ownsBackend("ufw") {
					ufwAdded, err = b.commands.Run(ctx, "ufw", "", "show", "added")
					if err != nil {
						return fmt.Errorf("检查 UFW 持久规则: %w", err)
					}
				}
			default:
				return fmt.Errorf("无法识别 UFW 状态")
			}
		} else if b.cfg.Backend == "ufw" || b.ownsBackend("ufw") {
			return fmt.Errorf("未找到 ufw 命令")
		}
	}
	if fdWanted || b.ownsBackend("firewalld") {
		if b.commands.Available("firewall-cmd") {
			status, checkErr := b.commands.Run(ctx, "firewall-cmd", "", "--state")
			switch {
			case strings.TrimSpace(status) == "running":
				fdActive = true
			case firewalldInactive(status):
			default:
				if checkErr != nil {
					return fmt.Errorf("检查 firewalld 状态: %w", checkErr)
				}
				return fmt.Errorf("无法识别 firewalld 状态")
			}
		} else if b.cfg.Backend == "firewalld" || b.ownsBackend("firewalld") {
			return fmt.Errorf("未找到 firewall-cmd 命令")
		}
	}
	wanted := map[ownedRule]bool{}
	// 所有区域先检查，再执行写入；冲突不能造成半套来源策略。
	if ufwActive && ufwWanted {
		if hasSources(allows) {
			status, err := b.commands.Run(ctx, "ufw", "", "status", "verbose")
			if err != nil {
				return err
			}
			if !strings.Contains(status, "deny (incoming)") && !strings.Contains(status, "reject (incoming)") {
				return pending("无法确认 UFW 默认入站策略会拦截未授权来源，请确认防火墙默认策略；未修改来源规则")
			}
		}
		if err := b.checkUFWSourceConflicts(allows, ufwRules); err != nil {
			return err
		}
	}
	fdRules := map[string]map[string]bool{}
	var fdZones []string
	if fdActive && fdWanted && len(allows) > 0 {
		fdZones, err = b.firewalldZones(ctx)
		if err != nil {
			return err
		}
		for _, zone := range fdZones {
			rules, err := b.firewalldRules(ctx, zone)
			if err != nil {
				return err
			}
			fdRules[zone] = rules
			if err := b.checkFirewalldSourceConflicts(ctx, zone, allows, rules); err != nil {
				return err
			}
		}
	}
	if ufwActive && ufwWanted {
		for _, rule := range allows {
			owned := ownedRule{Backend: "ufw", Rule: rule}
			wanted[owned] = true
			if err := b.ensureUFW(ctx, owned, ufwRules); err != nil {
				return err
			}
		}
	}
	if fdActive && fdWanted && len(allows) > 0 {
		for _, zone := range fdZones {
			rules := fdRules[zone]
			for _, rule := range allows {
				owned := ownedRule{Backend: "firewalld", Zone: zone, Rule: rule}
				wanted[owned] = true
				if err := b.ensureFirewalld(ctx, owned, rules); err != nil {
					return err
				}
			}
		}
	}
	if err := b.applyRedirects(ctx, redirects); err != nil {
		return err
	}
	// 已撤销的转发先移除，再回收放行规则；仍被其他节点引用的规则继续保留。
	for _, owned := range append([]ownedRule(nil), b.state.Owned...) {
		if wanted[owned] || b.keepOwned(owned.Rule) {
			continue
		}
		switch owned.Backend {
		case "ufw":
			if err := b.removeUFW(ctx, owned, ufwRules, ufwAdded); err != nil {
				return err
			}
		case "firewalld":
			if !fdActive {
				return fmt.Errorf("firewalld 未运行，已有托管规则将在其恢复后清理")
			}
			rules := fdRules[owned.Zone]
			if rules == nil {
				rules, err = b.firewalldRules(ctx, owned.Zone)
				if err != nil {
					return err
				}
				fdRules[owned.Zone] = rules
			}
			if rules[b.richRule(owned.Rule)] {
				if _, err := b.commands.Run(ctx, "firewall-cmd", "", "--zone="+owned.Zone, "--remove-rich-rule="+b.richRule(owned.Rule)); err != nil {
					return err
				}
				delete(rules, b.richRule(owned.Rule))
			}
			if err := b.forget(owned); err != nil {
				return err
			}
		}
	}
	return nil
}

// 旧规则覆盖的交集仍在已确认来源内时可以保留，包括同端口的多个出口和双栈来源。
func retainedSourceAllowed(owned, wanted Rule, allows []Rule) bool {
	if owned.Source == "" {
		return false
	}
	from, to := max(owned.Ports.From, wanted.Ports.From), min(owned.Ports.To, wanted.Ports.To)
	for _, rule := range allows {
		if rule.Source == owned.Source && rule.Family == owned.Family && rule.Protocol == owned.Protocol &&
			(rule.Address == "" || rule.Address == owned.Address) && rule.Ports.From <= from && rule.Ports.To >= to {
			return true
		}
	}
	return false
}

func firewalldInactive(status string) bool {
	if strings.TrimSpace(status) == "not running" {
		return true
	}
	// 仅安装命令而未启动系统 D-Bus 的环境也属于未运行；权限错误仍须上报。
	return strings.Contains(status, "DBUS_ERROR") && strings.Contains(status, "Failed to connect to socket") &&
		(strings.Contains(status, "No such file or directory") || strings.Contains(status, "Connection refused"))
}

func (b *systemBackend) ownsBackend(backend string) bool {
	for _, rule := range b.state.Owned {
		if rule.Backend == backend {
			return true
		}
	}
	return false
}

func (b *systemBackend) firewalldZones(ctx context.Context) ([]string, error) {
	if b.cfg.Zone != "" {
		return []string{b.cfg.Zone}, nil
	}
	output, err := b.commands.Run(ctx, "firewall-cmd", "", "--get-active-zones")
	if err != nil {
		return nil, err
	}
	zones := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		zones[strings.Fields(line)[0]] = true
	}
	output, err = b.commands.Run(ctx, "firewall-cmd", "", "--get-default-zone")
	if err != nil {
		return nil, err
	}
	zones[strings.TrimSpace(output)] = true
	var result []string
	for zone := range zones {
		if zone == "" {
			return nil, fmt.Errorf("firewalld 未返回有效区域")
		}
		result = append(result, zone)
	}
	sort.Strings(result)
	return result, nil
}
