package firewall

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type ufwRule struct {
	Rule         Rule
	Action       string
	Comment      string
	ScopedSource bool
}

var ufwStatusLine = regexp.MustCompile(`^\[\s*\d+\]\s+(.+?)\s+((?:ALLOW|DENY|REJECT|LIMIT)(?: IN)?)\s+(.+?)\s*$`)
var ufwDestination = regexp.MustCompile(`^(?:(\S+)\s+)?(\d+)(?::(\d+))?(?:/(tcp|udp))?$`)
var ufwLogSuffix = regexp.MustCompile(`\s+\(log(?:-all)?\)$`)

func parseUFWStatus(output string) []ufwRule {
	var result []ufwRule
	for _, line := range strings.Split(output, "\n") {
		body, comment, _ := strings.Cut(line, "#")
		match := ufwStatusLine.FindStringSubmatch(strings.TrimSpace(body))
		if match == nil {
			continue
		}
		family := 4
		if strings.Contains(match[1]+match[3], "(v6)") {
			family = 6
		}
		destination := strings.TrimSpace(strings.ReplaceAll(match[1], "(v6)", ""))
		destination, _, scoped := strings.Cut(destination, " on ")
		source := strings.TrimSpace(strings.ReplaceAll(match[3], "(v6)", ""))
		source = strings.TrimSpace(ufwLogSuffix.ReplaceAllString(source, ""))
		// 明确写出 IPv6 地址时，UFW 不一定附加 (v6) 标记。
		for _, endpoint := range append(strings.Fields(destination), strings.Fields(source)...) {
			address, _, _ := strings.Cut(endpoint, "/")
			if ip, err := netip.ParseAddr(address); err == nil && ip.Is6() {
				family = 6
			}
		}
		if source == "Anywhere" || source == "0.0.0.0/0" || source == "::/0" {
			source = ""
		} else if prefix, err := netip.ParsePrefix(source); err == nil {
			if prefix.Bits() == prefix.Addr().BitLen() {
				source = prefix.Addr().String()
			} else {
				scoped, source = true, ""
			}
		}
		parts := ufwDestination.FindStringSubmatch(destination)
		if parts == nil {
			// 应用名、全端口或无法解析的入站规则也可能覆盖落地，不能忽略后声称已限制。
			for _, protocol := range []string{"tcp", "udp"} {
				rule := Rule{Family: family, Protocol: protocol}
				rule.Ports.From, rule.Ports.To = 1, 65535
				result = append(result, ufwRule{Rule: rule, Action: strings.Fields(match[2])[0],
					Comment: strings.TrimSpace(comment), ScopedSource: true})
			}
			continue
		}
		rule := Rule{Family: family, Address: parts[1], Source: source, Protocol: parts[4]}
		if rule.Address == "0.0.0.0/0" || rule.Address == "::/0" {
			rule.Address = ""
		}
		rule.Ports.From, _ = strconv.Atoi(parts[2])
		rule.Ports.To = rule.Ports.From
		if parts[3] != "" {
			rule.Ports.To, _ = strconv.Atoi(parts[3])
		}
		protocols := []string{rule.Protocol}
		if rule.Protocol == "" {
			protocols, scoped = []string{"tcp", "udp"}, true
		}
		for _, protocol := range protocols {
			rule.Protocol = protocol
			if !rule.valid() {
				// 地址网段、来源端口等无法精确还原时，至少保留端口占用供冲突检查。
				rule.Address, rule.Source, scoped = "", "", true
			}
			result = append(result, ufwRule{Rule: rule, Action: strings.Fields(match[2])[0], Comment: strings.TrimSpace(comment), ScopedSource: scoped})
		}
	}
	return result
}

// 拒绝、限流和无法精确还原的规则都可能影响自动放行，不能因其写法不同而跳过。
func checkUFWBlockingRules(wanted []Rule, existing []ufwRule) error {
	for _, rule := range wanted {
		for _, current := range existing {
			if current.Action != "ALLOW" && overlaps(rule, current.Rule) {
				return fmt.Errorf("UFW 端口 %s/%s 被现有 %s 规则覆盖，自动放行无法保证访问；请确认手工限制，原规则未修改", rule.Ports, rule.Protocol, current.Action)
			}
		}
	}
	return nil
}

func (b *systemBackend) ufwArgs(rule Rule, remove bool) []string {
	return b.ufwArgsWithComment(rule, remove, b.ufwComment(rule))
}

func (b *systemBackend) ufwArgsWithComment(rule Rule, remove bool, comment string) []string {
	any := "0.0.0.0/0"
	if rule.Family == 6 {
		any = "::/0"
	}
	address := rule.Address
	if address == "" {
		address = any
	}
	var args []string
	source := rule.Source
	if source == "" {
		source = any
	}
	if remove {
		args = append(args, "--force", "delete")
	}
	return append(args, "allow", "in", "proto", rule.Protocol, "from", source, "to", address,
		"port", strings.ReplaceAll(rule.Ports.String(), "-", ":"), "comment", comment)
}

func (b *systemBackend) ensureUFW(ctx context.Context, owned ownedRule, existing []ufwRule) error {
	if owned.Priority == -1 {
		return b.ensureUFWManaged(ctx, owned, existing)
	}
	if err := checkUFWBlockingRules([]Rule{owned.Rule}, existing); err != nil {
		return err
	}
	for _, rule := range existing {
		if rule.ScopedSource || rule.Rule != owned.Rule {
			continue
		}
		if b.ownsUFWComment(owned.Rule, rule.Comment) {
			if err := b.remember(owned); err != nil {
				return err
			}
			if rule.Comment != b.ufwComment(owned.Rule) {
				// UFW 对完全相同的规则原位更新注释，不改变规则顺序或短暂撤销放行。
				if _, err := b.commands.Run(ctx, "ufw", "", b.ufwArgs(owned.Rule, false)...); err != nil {
					return fmt.Errorf("更新 UFW 节点标识: %w", err)
				}
			}
			return nil
		}
		if strings.HasPrefix(rule.Comment, "yz-agent:") || strings.HasPrefix(rule.Comment, "yzboard-node:") {
			return fmt.Errorf("UFW 端口 %s/%s 已由另一个 Node 进程托管，请合并到同一进程或使用独立监听地址", owned.Rule.Ports, owned.Rule.Protocol)
		}
		// UFW 会改写完全相同规则的注释，因此已有手工规则直接复用，不认领。
		return b.forget(owned)
	}
	if err := b.remember(owned); err != nil {
		return err
	}
	if _, err := b.commands.Run(ctx, "ufw", "", b.ufwArgs(owned.Rule, false)...); err != nil {
		return fmt.Errorf("添加 UFW 放行规则: %w", err)
	}
	return nil
}

func (b *systemBackend) removeUFW(ctx context.Context, owned ownedRule, existing []ufwRule, added string) error {
	comments := map[string]bool{}
	for _, rule := range existing {
		if !rule.ScopedSource && rule.Rule == owned.Rule && rule.Action == "ALLOW" && b.ownsUFWComment(owned.Rule, rule.Comment) {
			comments[rule.Comment] = true
		}
	}
	for _, line := range strings.Split(added, "\n") {
		_, marker, ok := strings.Cut(line, " comment ")
		comment := strings.Trim(strings.TrimSpace(marker), "'\"")
		if ok && b.ownsUFWComment(owned.Rule, comment) && ufwAddedOwns(line, owned.Rule, comment) {
			comments[comment] = true
		}
	}
	for comment := range comments {
		if _, err := b.commands.Run(ctx, "ufw", "", b.ufwArgsWithComment(owned.Rule, true, comment)...); err != nil {
			return fmt.Errorf("删除 UFW 托管规则: %w", err)
		}
	}
	return b.forget(owned)
}

// UFW 停用时 status 不列规则，改为核对 show added 中的完整规则和归属标记。
func ufwAddedOwns(output string, wanted Rule, comment string) bool {
	for _, line := range strings.Split(output, "\n") {
		body, marker, ok := strings.Cut(strings.TrimSpace(line), " comment ")
		if !ok || strings.Trim(strings.TrimSpace(marker), "'\"") != comment {
			continue
		}
		parts := strings.Fields(body)
		if len(parts) < 3 || parts[0] != "ufw" || parts[1] != "allow" {
			continue
		}
		rule := Rule{Family: wanted.Family}
		valid := true
		endpoint := ""
		for i := 2; i < len(parts) && valid; i++ {
			switch parts[i] {
			case "in", "log", "log-all":
			case "from", "to", "port", "proto":
				if i+1 == len(parts) {
					valid = false
					break
				}
				key, value := parts[i], parts[i+1]
				i++
				switch key {
				case "from":
					endpoint = "from"
					if value == "any" || value == "0.0.0.0/0" && wanted.Family == 4 || value == "::/0" && wanted.Family == 6 {
						break
					}
					ip, err := netip.ParseAddr(value)
					valid = err == nil && ip.Is4() == (wanted.Family == 4)
					rule.Source = ip.String()
				case "to":
					endpoint = "to"
					if value != "any" && value != "0.0.0.0/0" && value != "::/0" {
						address, err := netip.ParseAddr(value)
						valid = err == nil
						rule.Address = address.String()
					} else if value != "any" {
						valid = value == "0.0.0.0/0" && wanted.Family == 4 || value == "::/0" && wanted.Family == 6
					}
				case "port":
					valid = endpoint != "from" && parseUFWPorts(&rule, value)
				case "proto":
					rule.Protocol = value
				}
			default:
				ports, protocol, ok := strings.Cut(parts[i], "/")
				valid = ok && parseUFWPorts(&rule, ports)
				rule.Protocol = protocol
			}
		}
		if valid && rule == wanted {
			return true
		}
	}
	return false
}

func parseUFWPorts(rule *Rule, text string) bool {
	first, last, _ := strings.Cut(text, ":")
	var err error
	rule.Ports.From, err = strconv.Atoi(first)
	if err != nil {
		return false
	}
	rule.Ports.To = rule.Ports.From
	if last != "" {
		rule.Ports.To, err = strconv.Atoi(last)
	}
	return err == nil && rule.Ports.Valid()
}
