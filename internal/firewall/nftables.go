package firewall

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var redirectMarker = regexp.MustCompile(`^redirect:[0-9a-f]{16}$`)

func (b *systemBackend) nftTable() string {
	if b.state.Namespace == "" {
		return "yz_node_" + b.scope
	}
	return "yz_agent_" + b.scope
}
func (b *systemBackend) nftOwner() string {
	if b.state.Namespace == "" {
		return "yzboard-node:" + b.scope
	}
	return "yz-agent:" + b.scope
}

func redirectComment(rule Rule) string {
	sum := sha256.Sum256([]byte(rule.key()))
	return fmt.Sprintf("redirect:%x", sum[:8])
}

func (b *systemBackend) nftScript(rules []Rule, replace bool) string {
	var script strings.Builder
	if replace {
		fmt.Fprintf(&script, "delete table inet %s\n", b.nftTable())
	}
	if len(rules) == 0 {
		return script.String()
	}
	fmt.Fprintf(&script, "table inet %s {\n comment \"%s\";\n chain ownership { counter comment \"%s\"; }\n chain prerouting {\n  type nat hook prerouting priority dstnat; policy accept;\n", b.nftTable(), b.nftOwner(), b.nftOwner())
	for _, rule := range rules {
		family := "ip"
		if rule.Family == 6 {
			family = "ip6"
		}
		fmt.Fprintf(&script, "  meta nfproto ipv%d fib daddr type local ", rule.Family)
		if rule.Address != "" {
			fmt.Fprintf(&script, "%s daddr %s ", family, rule.Address)
		}
		fmt.Fprintf(&script, "udp dport %s counter ", rule.Ports)
		if rule.Address == "" {
			fmt.Fprintf(&script, "redirect to :%d ", rule.Target)
		} else if rule.Family == 4 {
			fmt.Fprintf(&script, "dnat ip to %s:%d ", rule.Address, rule.Target)
		} else {
			fmt.Fprintf(&script, "dnat ip6 to [%s]:%d ", rule.Address, rule.Target)
		}
		fmt.Fprintf(&script, "comment \"%s\";\n", redirectComment(rule))
	}
	script.WriteString(" }\n}\n")
	return script.String()
}

func (b *systemBackend) syncNFT(ctx context.Context, rules []Rule) error {
	if !b.commands.Available("nft") {
		return fmt.Errorf("未找到 nft 命令，无法管理端口跳跃")
	}
	output, err := b.commands.Run(ctx, "nft", "", "--json", "list", "table", "inet", b.nftTable())
	exists := err == nil
	if err != nil && !(strings.Contains(output, "No such file or directory") && strings.Contains(output, b.nftTable())) {
		return fmt.Errorf("检查端口跳跃 nftables 表: %w", err)
	}
	if exists {
		matches, err := b.checkNFTTable(output, rules)
		if err != nil {
			return err
		}
		if len(rules) > 0 && matches {
			return nil
		}
	}
	if !exists && len(rules) == 0 {
		return nil
	}
	if _, err := b.commands.Run(ctx, "nft", b.nftScript(rules, exists), "--file", "-"); err != nil {
		return fmt.Errorf("应用端口跳跃 nftables 规则: %w", err)
	}
	return nil
}

func (b *systemBackend) checkNFTTable(output string, rules []Rule) (bool, error) {
	var raw struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return false, fmt.Errorf("解析 nftables 表: %w", err)
	}
	for _, entry := range raw.NFTables {
		for kind := range entry {
			if kind != "metainfo" && kind != "table" && kind != "chain" && kind != "rule" {
				return false, fmt.Errorf("端口跳跃专用表包含非托管对象")
			}
		}
	}
	var document struct {
		NFTables []struct {
			Table *struct {
				Name    string
				Family  string
				Comment string
			} `json:"table"`
			Chain *struct {
				Name   string
				Type   string
				Hook   string
				Prio   int
				Policy string
			} `json:"chain"`
			Rule *struct {
				Chain   string
				Comment string
			} `json:"rule"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		return false, fmt.Errorf("解析 nftables 表: %w", err)
	}
	found, chainMatches, chainCount := false, false, 0
	ownerChains, ownerMarkers := 0, 0
	comments := map[string]int{}
	for _, entry := range document.NFTables {
		if entry.Table != nil {
			if entry.Table.Name != b.nftTable() || entry.Table.Family != "inet" || entry.Table.Comment != "" && entry.Table.Comment != b.nftOwner() {
				return false, fmt.Errorf("同名 nftables 表不属于本 Node 实例，停止修改")
			}
			found = true
		}
		if entry.Chain != nil {
			if entry.Chain.Name == "ownership" && entry.Chain.Type == "" && entry.Chain.Hook == "" {
				ownerChains++
				continue
			}
			if entry.Chain.Name != "prerouting" {
				return false, fmt.Errorf("端口跳跃专用表包含非托管链")
			}
			chainCount++
			chainMatches = entry.Chain.Name == "prerouting" && entry.Chain.Type == "nat" && entry.Chain.Hook == "prerouting" && entry.Chain.Prio == -100 && entry.Chain.Policy == "accept"
		}
		if entry.Rule != nil {
			if entry.Rule.Chain == "ownership" && entry.Rule.Comment == b.nftOwner() {
				ownerMarkers++
				continue
			}
			if entry.Rule.Chain != "prerouting" || !redirectMarker.MatchString(entry.Rule.Comment) {
				return false, fmt.Errorf("端口跳跃专用表包含非托管规则")
			}
			comments[entry.Rule.Comment]++
		}
	}
	if !found {
		return false, fmt.Errorf("nftables 输出缺少专用表归属信息")
	}
	// nftables 1.0.6 的 JSON 不返回表注释，用不挂钩的专用链保存可校验的归属标记。
	if ownerChains != 1 || ownerMarkers != 1 {
		return false, fmt.Errorf("nftables 专用表缺少本实例的归属标记")
	}
	if !chainMatches || chainCount != 1 || len(comments) != len(rules) {
		return false, nil
	}
	for _, rule := range rules {
		if comments[redirectComment(rule)] != 1 {
			return false, nil
		}
	}
	return true, nil
}
