package firewall

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"sort"
	"strings"

	"go4.org/netipx"
)

type sourceNFTSpec struct {
	Sets  map[string][]string
	Rules []map[string]any
}

func nftSourceMatch(left, right any) map[string]any {
	return map[string]any{"match": map[string]any{"op": "==", "left": left, "right": right}}
}

func (b *systemBackend) sourceNFTSpec(plans []sourcePlan) sourceNFTSpec {
	spec := sourceNFTSpec{Sets: map[string][]string{}, Rules: []map[string]any{}}
	for _, plan := range plans {
		for _, family := range []int{4, 6} {
			prefixes := sourcePrefixes(plan, family)
			if len(prefixes) == 0 {
				continue
			}
			name := fmt.Sprintf("b_%s_%d", sourcePlanID(plan), family)
			for _, prefix := range prefixes {
				spec.Sets[name] = append(spec.Sets[name], prefix.String())
			}
			protocol := "ip"
			if family == 6 {
				protocol = "ip6"
			}
			for _, listener := range plan.Listeners {
				if listener.Family != family {
					continue
				}
				// nft 自动从 payload 推导地址族和传输协议，回读时会省略冗余的 meta 匹配。
				expressions := []any{}
				if listener.Address != "" {
					expressions = append(expressions, nftSourceMatch(map[string]any{"payload": map[string]any{"protocol": protocol, "field": "daddr"}}, listener.Address))
				}
				var ports any = listener.Ports.From
				if listener.Ports.From != listener.Ports.To {
					ports = map[string]any{"range": []int{listener.Ports.From, listener.Ports.To}}
				}
				expressions = append(expressions,
					nftSourceMatch(map[string]any{"payload": map[string]any{"protocol": listener.Protocol, "field": "dport"}}, ports),
					nftSourceMatch(map[string]any{"payload": map[string]any{"protocol": protocol, "field": "saddr"}}, "@"+name),
					map[string]any{"drop": nil})
				spec.Rules = append(spec.Rules, map[string]any{"family": "inet", "table": b.sourceTable(), "chain": "input", "expr": expressions, "comment": b.sourceOwner()})
			}
		}
	}
	return spec
}

func (b *systemBackend) sourceNFTScript(spec sourceNFTSpec, replace bool) string {
	table := b.sourceTable()
	// JSON 输入避免字符串拼接协议表达式；所有用户地址均已按 IP/网段校验。
	commands := []any{
		map[string]any{"add": map[string]any{"table": map[string]any{"family": "inet", "name": table, "comment": b.sourceOwner()}}},
		map[string]any{"add": map[string]any{"chain": map[string]any{"family": "inet", "table": table, "name": "ownership"}}},
		map[string]any{"add": map[string]any{"rule": map[string]any{"family": "inet", "table": table, "chain": "ownership", "expr": []any{map[string]any{"counter": nil}}, "comment": b.sourceOwner()}}},
		map[string]any{"add": map[string]any{"chain": map[string]any{"family": "inet", "table": table, "name": "input", "type": "filter", "hook": "input", "prio": -10, "policy": "accept"}}},
	}
	if replace {
		commands = append([]any{map[string]any{"delete": map[string]any{"table": map[string]any{"family": "inet", "name": table}}}}, commands...)
	}
	keys := make([]string, 0, len(spec.Sets))
	for name := range spec.Sets {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		typeName := "ipv4_addr"
		if strings.HasSuffix(name, "_6") {
			typeName = "ipv6_addr"
		}
		var elements []any
		for _, raw := range spec.Sets[name] {
			prefix := netip.MustParsePrefix(raw)
			elements = append(elements, map[string]any{"prefix": map[string]any{"addr": prefix.Addr().String(), "len": prefix.Bits()}})
		}
		commands = append(commands, map[string]any{"add": map[string]any{"set": map[string]any{
			"family": "inet", "table": table, "name": name, "type": typeName, "flags": []string{"interval"}, "elem": elements,
		}}})
	}
	for _, rule := range spec.Rules {
		commands = append(commands, map[string]any{"add": map[string]any{"rule": rule}})
	}
	data, _ := json.Marshal(map[string]any{"nftables": commands})
	return string(data)
}

func (b *systemBackend) readSourceNFT(ctx context.Context) ([]map[string]json.RawMessage, bool, error) {
	output, err := b.commands.Run(ctx, "nft", "", "--json", "list", "tables")
	if err != nil {
		return nil, false, fmt.Errorf("读取 nftables 来源过滤权限: %w", err)
	}
	var listing struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(output), &listing); err != nil {
		return nil, false, fmt.Errorf("解析 nftables 表清单: %w", err)
	}
	found := false
	for _, object := range listing.NFTables {
		var table struct{ Family, Name string }
		if data := object["table"]; data != nil {
			if err := json.Unmarshal(data, &table); err != nil {
				return nil, false, err
			}
			found = found || table.Family == "inet" && table.Name == b.sourceTable()
		}
	}
	if !found {
		return nil, false, nil
	}
	output, err = b.commands.Run(ctx, "nft", "", "--json", "--numeric-priority", "list", "table", "inet", b.sourceTable())
	if err != nil {
		return nil, true, err
	}
	// encoding/json 会复用切片里的 map，必须清空表清单，避免保留上次查询的其他表键。
	listing.NFTables = nil
	if err := json.Unmarshal([]byte(output), &listing); err != nil {
		return nil, true, err
	}
	marker := false
	for _, object := range listing.NFTables {
		for kind, data := range object {
			if kind == "metainfo" {
				continue
			}
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				return nil, true, err
			}
			owned := false
			switch kind {
			case "table":
				owned = value["family"] == "inet" && value["name"] == b.sourceTable()
			case "chain":
				owned = value["table"] == b.sourceTable() && (value["name"] == "input" || value["name"] == "ownership")
			case "set":
				name, _ := value["name"].(string)
				owned = value["table"] == b.sourceTable() && strings.HasPrefix(name, "b_")
			case "rule":
				owned = value["table"] == b.sourceTable() && value["comment"] == b.sourceOwner()
				marker = marker || owned && value["chain"] == "ownership"
			}
			if !owned {
				return nil, true, fmt.Errorf("同名 nftables 来源表包含非本实例对象（%s），停止修改", kind)
			}
		}
	}
	if !marker {
		return nil, true, fmt.Errorf("nftables 来源表缺少本实例归属标记")
	}
	return listing.NFTables, true, nil
}

func canonicalNFTPrefixes(elements json.RawMessage) ([]string, error) {
	var values []any
	if err := json.Unmarshal(elements, &values); err != nil {
		return nil, err
	}
	var builder netipx.IPSetBuilder
	for _, value := range values {
		switch item := value.(type) {
		case string:
			if addr, err := netip.ParseAddr(item); err == nil {
				builder.Add(addr)
			} else if prefix, err := netip.ParsePrefix(item); err == nil {
				builder.AddPrefix(prefix)
			} else {
				return nil, fmt.Errorf("nftables 来源集合含无效地址")
			}
		case map[string]any:
			if raw, ok := item["prefix"].(map[string]any); ok {
				addr, err := netip.ParseAddr(fmt.Sprint(raw["addr"]))
				bits, valid := raw["len"].(float64)
				if err != nil || !valid || int(bits) < 0 || int(bits) > addr.BitLen() {
					return nil, fmt.Errorf("nftables 来源集合含无效前缀")
				}
				builder.AddPrefix(netip.PrefixFrom(addr, int(bits)))
			} else if raw, ok := item["range"].([]any); ok && len(raw) == 2 {
				first, err1 := netip.ParseAddr(fmt.Sprint(raw[0]))
				last, err2 := netip.ParseAddr(fmt.Sprint(raw[1]))
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("nftables 来源集合含无效范围")
				}
				builder.AddRange(netipx.IPRangeFrom(first, last))
			} else {
				return nil, fmt.Errorf("nftables 来源集合含未知元素")
			}
		default:
			return nil, fmt.Errorf("nftables 来源集合含未知元素")
		}
	}
	set, err := builder.IPSet()
	if err != nil {
		return nil, err
	}
	var result []string
	for _, prefix := range set.Prefixes() {
		result = append(result, prefix.String())
	}
	return result, nil
}

func (b *systemBackend) sourceNFTMatches(objects []map[string]json.RawMessage, spec sourceNFTSpec) bool {
	sets := map[string][]string{}
	var rules []map[string]any
	input, ownership := false, false
	for _, object := range objects {
		if raw := object["chain"]; raw != nil {
			var chain struct {
				Name, Type, Hook, Policy string
				Prio                     int
			}
			if json.Unmarshal(raw, &chain) != nil {
				return false
			}
			if chain.Name == "input" {
				input = chain.Type == "filter" && chain.Hook == "input" && chain.Prio == -10 && chain.Policy == "accept"
			} else if chain.Name == "ownership" {
				ownership = chain.Type == "" && chain.Hook == ""
			}
		}
		if raw := object["set"]; raw != nil {
			var set struct {
				Name, Type string
				Flags      []string
				Elem       json.RawMessage
			}
			if json.Unmarshal(raw, &set) != nil || len(set.Flags) != 1 || set.Flags[0] != "interval" {
				return false
			}
			expectedType := "ipv4_addr"
			if strings.HasSuffix(set.Name, "_6") {
				expectedType = "ipv6_addr"
			}
			if set.Type != expectedType {
				return false
			}
			var err error
			sets[set.Name], err = canonicalNFTPrefixes(set.Elem)
			if err != nil {
				return false
			}
		}
		if raw := object["rule"]; raw != nil {
			var rule map[string]any
			if json.Unmarshal(raw, &rule) != nil {
				return false
			}
			if rule["chain"] == "input" {
				delete(rule, "handle")
				rules = append(rules, rule)
			}
		}
	}
	// 将期望集合也正规化，兼容 nft 对相邻区间和单地址的显示方式。
	expectedSets := map[string][]string{}
	for name, prefixes := range spec.Sets {
		data, _ := json.Marshal(prefixes)
		expectedSets[name], _ = canonicalNFTPrefixes(data)
	}
	actualRules, _ := json.Marshal(rules)
	expectedRules, _ := json.Marshal(spec.Rules)
	if len(rules) == 0 {
		actualRules = []byte("[]")
	}
	return input && ownership && reflect.DeepEqual(sets, expectedSets) && string(actualRules) == string(expectedRules)
}

func (b *systemBackend) syncSourceNFT(ctx context.Context, plans []sourcePlan) error {
	if !b.commands.Available("nft") {
		return fmt.Errorf("未找到 nft 命令")
	}
	objects, exists, err := b.readSourceNFT(ctx)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		if !exists {
			return nil
		}
		_, err = b.commands.Run(ctx, "nft", "", "delete", "table", "inet", b.sourceTable())
		if err != nil {
			return err
		}
		_, exists, err = b.readSourceNFT(ctx)
		if err == nil && exists {
			return fmt.Errorf("nftables 来源表删除后仍存在")
		}
		return err
	}
	spec := b.sourceNFTSpec(plans)
	if exists && b.sourceNFTMatches(objects, spec) {
		return nil
	}
	if _, err := b.commands.Run(ctx, "nft", b.sourceNFTScript(spec, exists), "--json", "--file", "-"); err != nil {
		return fmt.Errorf("写入 nftables 来源规则: %w", err)
	}
	objects, exists, err = b.readSourceNFT(ctx)
	if err != nil {
		return err
	}
	if !exists || !b.sourceNFTMatches(objects, spec) {
		return fmt.Errorf("nftables 来源规则回读核验未通过")
	}
	return nil
}
