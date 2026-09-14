package firewall

import (
	"context"
	"fmt"
	"strings"
)

func (b *systemBackend) iptablesChain() string {
	if b.state.Namespace == "" {
		return "YZH_" + b.scope
	}
	return "YZ_AGENT_" + b.scope
}

func (b *systemBackend) syncIPTables(ctx context.Context, rules []Rule) error {
	for _, family := range b.state.RedirectFamilies {
		name := "iptables"
		if family == 6 {
			name = "ip6tables"
		}
		if !b.commands.Available(name) || !b.commands.Available(name+"-restore") {
			return fmt.Errorf("端口跳跃需要 %s 和 %s-restore", name, name)
		}
		var desired []Rule
		for _, rule := range rules {
			if rule.Family == family {
				desired = append(desired, rule)
			}
		}
		if err := b.syncIPTablesFamily(ctx, name, desired); err != nil {
			return err
		}
	}
	return nil
}

func (b *systemBackend) syncIPTablesFamily(ctx context.Context, name string, rules []Rule) error {
	chain, owner := b.iptablesChain(), b.nftOwner()
	// iptables-nft 查询不存在的单链可能返回“incompatible”，改为从完整 NAT 表准确定位。
	output, err := b.commands.Run(ctx, name, "", "-w", "5", "-t", "nat", "-S")
	if err != nil {
		return fmt.Errorf("检查 %s 端口跳跃链: %w", name, err)
	}
	exists := false
	var chainRules []string
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "-N "+chain {
			exists = true
		}
		if strings.HasPrefix(line, "-A "+chain+" ") {
			chainRules = append(chainRules, line)
		}
	}
	if exists {
		hasMarker := false
		for _, line := range chainRules {
			if !strings.Contains(line, "--comment "+owner+" ") && !strings.Contains(line, "--comment \""+owner+"\" ") {
				return fmt.Errorf("同名 iptables 链包含非托管规则，停止修改")
			}
			hasMarker = true
		}
		if !hasMarker {
			return fmt.Errorf("同名 iptables 链缺少 Node 归属标记，停止修改")
		}
	}
	jump := []string{"PREROUTING", "-m", "addrtype", "--dst-type", "LOCAL", "-m", "comment", "--comment", owner, "-j", chain}
	checkJump := func() (bool, error) {
		args := append([]string{"-w", "5", "-t", "nat", "-C"}, jump...)
		out, err := b.commands.Run(ctx, name, "", args...)
		if err == nil {
			return true, nil
		}
		if strings.Contains(out, "Bad rule") || strings.Contains(out, "No chain/target/match by that name") {
			return false, nil
		}
		return false, err
	}
	if len(rules) == 0 {
		if !exists {
			return nil
		}
		for {
			present, err := checkJump()
			if err != nil {
				return err
			}
			if !present {
				break
			}
			if _, err := b.commands.Run(ctx, name, "", append([]string{"-w", "5", "-t", "nat", "-D"}, jump...)...); err != nil {
				return err
			}
		}
		// 清空与删除同一事务提交，避免中断后遗留没有归属标记的空链。
		script := fmt.Sprintf("*nat\n-F %s\n-X %s\nCOMMIT\n", chain, chain)
		_, err := b.commands.Run(ctx, name+"-restore", script, "--wait", "5", "--noflush")
		return err
	}
	var script strings.Builder
	fmt.Fprintf(&script, "*nat\n:%s - [0:0]\n-F %s\n", chain, chain)
	for _, rule := range rules {
		fmt.Fprintf(&script, "-A %s -p udp ", chain)
		if rule.Address != "" {
			fmt.Fprintf(&script, "-d %s ", rule.Address)
		}
		fmt.Fprintf(&script, "--dport %s -m comment --comment %s ", strings.ReplaceAll(rule.Ports.String(), "-", ":"), owner)
		if rule.Address == "" {
			fmt.Fprintf(&script, "-j REDIRECT --to-ports %d\n", rule.Target)
		} else if rule.Family == 4 {
			fmt.Fprintf(&script, "-j DNAT --to-destination %s:%d\n", rule.Address, rule.Target)
		} else {
			fmt.Fprintf(&script, "-j DNAT --to-destination [%s]:%d\n", rule.Address, rule.Target)
		}
	}
	fmt.Fprintf(&script, "-A %s -m comment --comment %s -j RETURN\nCOMMIT\n", chain, owner)
	if _, err := b.commands.Run(ctx, name+"-restore", script.String(), "--wait", "5", "--noflush"); err != nil {
		return fmt.Errorf("更新 %s 专用转发链: %w", name, err)
	}
	present, err := checkJump()
	if err != nil {
		return err
	}
	if !present {
		_, err = b.commands.Run(ctx, name, "", append([]string{"-w", "5", "-t", "nat", "-A"}, jump...)...)
	}
	return err
}
