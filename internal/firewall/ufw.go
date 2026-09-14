package firewall

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

type ufwRule struct {
	Rule    Rule
	Action  string
	Comment string
}

var ufwStatusLine = regexp.MustCompile(`^\[\s*\d+\]\s+(.+?)\s+((?:ALLOW|DENY|REJECT|LIMIT)(?: IN)?)\s+(.+?)\s*$`)
var ufwDestination = regexp.MustCompile(`^(?:(\S+)\s+)?(\d+)(?::(\d+))?/(tcp|udp)$`)
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
		source := strings.TrimSpace(strings.ReplaceAll(match[3], "(v6)", ""))
		source = strings.TrimSpace(ufwLogSuffix.ReplaceAllString(source, ""))
		if source != "Anywhere" && source != "0.0.0.0/0" && source != "::/0" {
			continue
		}
		parts := ufwDestination.FindStringSubmatch(destination)
		if parts == nil {
			continue
		}
		rule := Rule{Family: family, Address: parts[1], Protocol: parts[4]}
		if rule.Address == "0.0.0.0/0" || rule.Address == "::/0" {
			rule.Address = ""
		}
		rule.Ports.From, _ = strconv.Atoi(parts[2])
		rule.Ports.To = rule.Ports.From
		if parts[3] != "" {
			rule.Ports.To, _ = strconv.Atoi(parts[3])
		}
		if rule.valid() {
			result = append(result, ufwRule{Rule: rule, Action: strings.Fields(match[2])[0], Comment: strings.TrimSpace(comment)})
		}
	}
	return result
}

func (b *systemBackend) ufwComment(rule Rule) string {
	sum := sha256.Sum256([]byte(rule.key()))
	return fmt.Sprintf("%s:%x", b.nftOwner(), sum[:6])
}

func (b *systemBackend) ufwArgs(rule Rule, remove bool) []string {
	any := "0.0.0.0/0"
	if rule.Family == 6 {
		any = "::/0"
	}
	address := rule.Address
	if address == "" {
		address = any
	}
	var args []string
	if remove {
		args = append(args, "--force", "delete")
	}
	return append(args, "allow", "in", "proto", rule.Protocol, "from", any, "to", address,
		"port", strings.ReplaceAll(rule.Ports.String(), "-", ":"), "comment", b.ufwComment(rule))
}

func (b *systemBackend) ensureUFW(ctx context.Context, owned ownedRule, existing []ufwRule) error {
	for _, rule := range existing {
		if rule.Rule != owned.Rule {
			continue
		}
		if rule.Action != "ALLOW" {
			return fmt.Errorf("UFW 端口 %s/%s 已有 %s 规则，请先调整冲突规则", owned.Rule.Ports, owned.Rule.Protocol, rule.Action)
		}
		if rule.Comment == b.ufwComment(owned.Rule) {
			return b.remember(owned)
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
	comment := b.ufwComment(owned.Rule)
	found := false
	for _, rule := range existing {
		if rule.Rule == owned.Rule && rule.Action == "ALLOW" && rule.Comment == comment {
			found = true
		}
	}
	if ufwAddedOwns(added, owned.Rule, comment) {
		found = true
	}
	if found {
		if _, err := b.commands.Run(ctx, "ufw", "", b.ufwArgs(owned.Rule, true)...); err != nil {
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
					valid = value == "any" || value == "0.0.0.0/0" && wanted.Family == 4 || value == "::/0" && wanted.Family == 6
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
