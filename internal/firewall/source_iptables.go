package firewall

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
)

func (b *systemBackend) ownsSourceSetName(name string) bool {
	prefix := "YZS_" + b.sourceID() + "_"
	if !strings.HasPrefix(name, prefix) || len(name) > 31 {
		return false
	}
	for _, char := range strings.TrimPrefix(name, prefix) {
		if char != '_' && char != 't' && !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return len(name) >= len(prefix)+12
}

func ipsetSourcePrefixes(plan sourcePlan, family int) []string {
	var result []string
	for _, prefix := range sourcePrefixes(plan, family) {
		if prefix.Bits() == 0 {
			// hash:net 不接受 /0；两个 /1 与原集合等价。
			if family == 4 {
				result = append(result, "0.0.0.0/1", "128.0.0.0/1")
			} else {
				result = append(result, "::/1", "8000::/1")
			}
		} else {
			result = append(result, prefix.String())
		}
	}
	sort.Strings(result)
	return slices.Compact(result)
}

func (b *systemBackend) sourceSetName(prefixes []string, family int) string {
	sum := sha256.Sum256([]byte(strings.Join(prefixes, "\n")))
	return fmt.Sprintf("YZS_%s_%x_%d", b.sourceID(), sum[:5], family)
}

func (b *systemBackend) rememberSourceSet(name string) error {
	if slices.Contains(b.state.SourceSets, name) {
		return nil
	}
	b.state.SourceSets = append(b.state.SourceSets, name)
	return b.save()
}

func (b *systemBackend) forgetSourceSet(name string) error {
	b.state.SourceSets = slices.DeleteFunc(b.state.SourceSets, func(value string) bool { return value == name })
	return b.save()
}

func (b *systemBackend) sourceSetNames(ctx context.Context) (map[string]bool, error) {
	output, err := b.commands.Run(ctx, "ipset", "", "list", "-name")
	if err != nil {
		return nil, fmt.Errorf("读取 ipset 权限: %w", err)
	}
	result := map[string]bool{}
	for _, name := range strings.Fields(output) {
		result[name] = true
	}
	return result, nil
}

func (b *systemBackend) sourceSetMatches(ctx context.Context, name string, prefixes []string, family int) (bool, error) {
	output, err := b.commands.Run(ctx, "ipset", "", "save", name)
	if err != nil {
		return false, err
	}
	wantFamily := "inet"
	if family == 6 {
		wantFamily = "inet6"
	}
	created := false
	var actual []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "#" {
			continue
		}
		if len(fields) >= 5 && fields[0] == "create" && fields[1] == name && fields[2] == "hash:net" {
			for index := 3; index+1 < len(fields); index++ {
				if fields[index] == "family" && fields[index+1] == wantFamily {
					created = true
				}
				if fields[index] == "timeout" {
					return false, nil
				}
			}
		} else if len(fields) == 3 && fields[0] == "add" && fields[1] == name {
			prefix, err := netip.ParsePrefix(fields[2])
			if err != nil {
				addr, addrErr := netip.ParseAddr(fields[2])
				if addrErr != nil {
					return false, nil
				}
				prefix = netip.PrefixFrom(addr, addr.BitLen())
			}
			actual = append(actual, prefix.String())
		} else {
			return false, nil
		}
	}
	sort.Strings(actual)
	return created && slices.Equal(actual, prefixes), nil
}

func sourceSetScript(name string, prefixes []string, family int) string {
	wantFamily := "inet"
	if family == 6 {
		wantFamily = "inet6"
	}
	var script strings.Builder
	fmt.Fprintf(&script, "create %s hash:net family %s maxelem 262144\n", name, wantFamily)
	for _, prefix := range prefixes {
		fmt.Fprintf(&script, "add %s %s\n", name, prefix)
	}
	return script.String()
}

func (b *systemBackend) ensureSourceSet(ctx context.Context, name string, prefixes []string, family int) error {
	if len(prefixes) > 262144 {
		return fmt.Errorf("来源 IP 集合超出容量")
	}
	names, err := b.sourceSetNames(ctx)
	if err != nil {
		return err
	}
	if names[name] && !slices.Contains(b.state.SourceSets, name) {
		return fmt.Errorf("同名 ipset 集合不属于本实例，停止修改")
	}
	if err := b.rememberSourceSet(name); err != nil {
		return err
	}
	if !names[name] {
		if _, err := b.commands.Run(ctx, "ipset", sourceSetScript(name, prefixes, family), "restore"); err != nil {
			return err
		}
	} else {
		matches, err := b.sourceSetMatches(ctx, name, prefixes, family)
		if err != nil {
			return err
		}
		if matches {
			return nil
		}
		temporary := name + "_t"
		if names[temporary] {
			if !slices.Contains(b.state.SourceSets, temporary) {
				return fmt.Errorf("临时 ipset 集合存在归属冲突")
			}
			if _, err := b.commands.Run(ctx, "ipset", "", "destroy", temporary); err != nil {
				return err
			}
		}
		if err := b.rememberSourceSet(temporary); err != nil {
			return err
		}
		if _, err := b.commands.Run(ctx, "ipset", sourceSetScript(temporary, prefixes, family), "restore"); err != nil {
			return err
		}
		// 使用交换更新被引用的集合，避免先清空活动集合形成放行窗口。
		if _, err := b.commands.Run(ctx, "ipset", "", "swap", temporary, name); err != nil {
			return err
		}
		if _, err := b.commands.Run(ctx, "ipset", "", "destroy", temporary); err != nil {
			return err
		}
		if err := b.forgetSourceSet(temporary); err != nil {
			return err
		}
	}
	matches, err := b.sourceSetMatches(ctx, name, prefixes, family)
	if err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("ipset 来源集合回读核验未通过")
	}
	return nil
}

func (b *systemBackend) sourceIPTablesRules(plans []sourcePlan, family int) []string {
	var result []string
	for _, plan := range plans {
		prefixes := ipsetSourcePrefixes(plan, family)
		if len(prefixes) == 0 {
			continue
		}
		name := b.sourceSetName(prefixes, family)
		for _, listener := range plan.Listeners {
			if listener.Family != family {
				continue
			}
			line := "-A " + b.sourceChain()
			if listener.Address != "" {
				line += " -d " + listener.Address + "/" + fmt.Sprint(netip.MustParseAddr(listener.Address).BitLen())
			}
			line += " -p " + listener.Protocol
			line += " -m " + listener.Protocol + " --dport " + strings.ReplaceAll(listener.Ports.String(), "-", ":")
			line += " -m set --match-set " + name + " src -m comment --comment " + b.sourceOwner() + " -j DROP"
			result = append(result, line)
		}
	}
	return append(result, "-A "+b.sourceChain()+" -m comment --comment "+b.sourceOwner()+" -j RETURN")
}

type sourceIPTablesState struct {
	exists bool
	rules  []string
	jumps  []string
	first  bool
}

func normalizeIPTablesLine(line string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(line, "\"", "")), " ")
}

func (b *systemBackend) readSourceIPTables(ctx context.Context, name string) (sourceIPTablesState, error) {
	// mangle/INPUT 位于 UFW 的 filter 放行之前，已有 ACCEPT 不能绕过来源丢弃。
	output, err := b.commands.Run(ctx, name, "", "-w", "5", "-t", "mangle", "-S")
	if err != nil {
		return sourceIPTablesState{}, err
	}
	var state sourceIPTablesState
	first := true
	for _, raw := range strings.Split(output, "\n") {
		line := normalizeIPTablesLine(raw)
		if line == "-N "+b.sourceChain() {
			state.exists = true
		}
		if strings.HasPrefix(line, "-A "+b.sourceChain()+" ") {
			if !strings.Contains(line, "--comment "+b.sourceOwner()+" ") {
				return state, fmt.Errorf("iptables 来源链含非本实例规则")
			}
			state.rules = append(state.rules, line)
		}
		if strings.Contains(line+" ", "-j "+b.sourceChain()+" ") || strings.Contains(line+" ", "-g "+b.sourceChain()+" ") {
			expected := "-A INPUT -m comment --comment " + b.sourceOwner() + " -j " + b.sourceChain()
			if line != expected {
				return state, fmt.Errorf("iptables 来源链被其他规则引用，停止修改")
			}
			state.jumps = append(state.jumps, line)
			state.first = first
		}
		if strings.HasPrefix(line, "-A INPUT ") {
			first = false
		}
	}
	if state.exists && len(state.rules) == 0 {
		return state, fmt.Errorf("iptables 来源链缺少归属标记")
	}
	return state, nil
}

func (b *systemBackend) syncSourceIPTablesFamily(ctx context.Context, name string, plans []sourcePlan) error {
	state, err := b.readSourceIPTables(ctx, name)
	if err != nil {
		return err
	}
	family := 4
	if name == "ip6tables" {
		family = 6
	}
	wanted := b.sourceIPTablesRules(plans, family)
	if len(plans) > 0 && state.first && len(state.jumps) == 1 && slices.Equal(state.rules, wanted) {
		return nil
	}
	if len(plans) == 0 && !state.exists && len(state.jumps) == 0 {
		return nil
	}
	var script strings.Builder
	script.WriteString("*mangle\n")
	if len(plans) > 0 {
		fmt.Fprintf(&script, ":%s - [0:0]\n", b.sourceChain())
	}
	for _, jump := range state.jumps {
		script.WriteString(strings.Replace(jump, "-A INPUT ", "-D INPUT ", 1) + "\n")
	}
	if state.exists {
		fmt.Fprintf(&script, "-F %s\n", b.sourceChain())
	}
	if len(plans) == 0 {
		if state.exists {
			fmt.Fprintf(&script, "-X %s\n", b.sourceChain())
		}
	} else {
		for _, line := range wanted {
			script.WriteString(line + "\n")
		}
		fmt.Fprintf(&script, "-I INPUT 1 -m comment --comment %s -j %s\n", b.sourceOwner(), b.sourceChain())
	}
	script.WriteString("COMMIT\n")
	if _, err := b.commands.Run(ctx, name+"-restore", script.String(), "--wait", "5", "--noflush"); err != nil {
		return err
	}
	state, err = b.readSourceIPTables(ctx, name)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		if state.exists || len(state.jumps) != 0 {
			return fmt.Errorf("iptables 来源链清理核验未通过")
		}
	} else if !state.first || len(state.jumps) != 1 || !slices.Equal(state.rules, wanted) {
		return fmt.Errorf("iptables 来源规则回读核验未通过")
	}
	return nil
}

func (b *systemBackend) syncSourceIPTables(ctx context.Context, plans []sourcePlan) error {
	if !b.sourceIPTablesAvailable() {
		return fmt.Errorf("缺少 iptables/ip6tables、restore 或 ipset")
	}
	desiredSets := map[string]bool{}
	for _, plan := range plans {
		for _, family := range []int{4, 6} {
			prefixes := ipsetSourcePrefixes(plan, family)
			if len(prefixes) == 0 {
				continue
			}
			name := b.sourceSetName(prefixes, family)
			if desiredSets[name] {
				continue
			}
			if err := b.ensureSourceSet(ctx, name, prefixes, family); err != nil {
				return err
			}
			desiredSets[name] = true
		}
	}
	for _, name := range []string{"iptables", "ip6tables"} {
		if err := b.syncSourceIPTablesFamily(ctx, name, plans); err != nil {
			return err
		}
	}
	names, err := b.sourceSetNames(ctx)
	if err != nil {
		return err
	}
	for _, name := range slices.Clone(b.state.SourceSets) {
		if desiredSets[name] {
			continue
		}
		if names[name] {
			if _, err := b.commands.Run(ctx, "ipset", "", "destroy", name); err != nil {
				return err
			}
		}
		if err := b.forgetSourceSet(name); err != nil {
			return err
		}
	}
	return nil
}
