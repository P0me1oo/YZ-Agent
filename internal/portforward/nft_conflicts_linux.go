//go:build linux

package portforward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Socket probes cannot see NAT rules. Inspect other tables before allocating a
// new listener; never remove or rewrite another owner's rule.
func (m *Manager) checkNFTConflicts(ctx context.Context, rules []resolvedRule) error {
	if len(rules) == 0 {
		return nil
	}
	raw, err := m.run(ctx, "nft", []string{"-j", "list", "ruleset"}, "")
	if err != nil {
		return errors.New("cannot inspect existing NAT port allocations")
	}
	return nftConflicts(raw, m.table, rules)
}
func nftConflicts(raw []byte, own string, rules []resolvedRule) error {
	var doc struct {
		Items []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return errors.New("cannot parse nftables ruleset")
	}
	type set struct {
		Family, Table, Name string
		Elem                any
	}
	sets := map[string]any{}
	for _, item := range doc.Items {
		if v, ok := item["set"]; ok {
			var s set
			if json.Unmarshal(v, &s) == nil {
				sets[s.Family+"/"+s.Table+"/"+s.Name] = s.Elem
			}
		}
	}
	for _, item := range doc.Items {
		v, ok := item["rule"]
		if !ok {
			continue
		}
		var rule struct {
			Family, Table string
			Expr          []map[string]any
		}
		if json.Unmarshal(v, &rule) != nil {
			return errors.New("cannot parse nftables rule")
		}
		if rule.Family == "inet" && rule.Table == own {
			continue
		}
		nat := false
		for _, expr := range rule.Expr {
			for _, key := range []string{"dnat", "redirect", "tproxy"} {
				if _, ok := expr[key]; ok {
					nat = true
				}
			}
		}
		if !nat {
			continue
		}
		for _, candidate := range rules {
			matches, known := true, false
			for _, expr := range rule.Expr {
				match, ok := expr["match"].(map[string]any)
				if !ok {
					continue
				}
				left, _ := match["left"].(map[string]any)
				payload, _ := left["payload"].(map[string]any)
				if payload["field"] != "dport" {
					continue
				}
				known = true
				protocol, _ := payload["protocol"].(string)
				if protocol != "" && !strings.Contains(candidate.Network, protocol) {
					matches = false
					break
				}
				right := match["right"]
				if ref, ok := right.(string); ok && strings.HasPrefix(ref, "@") {
					right = sets[rule.Family+"/"+rule.Table+"/"+strings.TrimPrefix(ref, "@")]
					if right == nil {
						return errors.New("cannot resolve a port set used by an existing NAT rule")
					}
				}
				op, _ := match["op"].(string)
				hit, parsed := portContains(right, candidate.ListenPort)
				if !parsed {
					return errors.New("existing NAT port expression cannot be verified")
				}
				if op == "!=" {
					hit = !hit
				} else if op != "==" && op != "in" {
					return errors.New("existing NAT port comparison cannot be verified")
				}
				if !hit {
					matches = false
					break
				}
			}
			if matches {
				if !known {
					return errors.New("existing NAT rule has no verifiable destination-port boundary")
				}
				return fmt.Errorf("forwarding port %d conflicts with an existing NAT rule", candidate.ListenPort)
			}
		}
	}
	return nil
}
func portContains(v any, port int) (bool, bool) {
	switch x := v.(type) {
	case float64:
		return x == float64(port), true
	case []any:
		for _, part := range x {
			hit, known := portContains(part, port)
			if !known {
				return false, false
			}
			if hit {
				return true, true
			}
		}
		return false, true
	case map[string]any:
		if values, ok := x["set"]; ok {
			return portContains(values, port)
		}
		if values, ok := x["range"].([]any); ok && len(values) == 2 {
			a, aOK := values[0].(float64)
			b, bOK := values[1].(float64)
			return float64(port) >= a && float64(port) <= b, aOK && bOK
		}
		if elem, ok := x["elem"]; ok {
			if obj, ok := elem.(map[string]any); ok {
				return portContains(obj["val"], port)
			}
			return portContains(elem, port)
		}
	}
	return false, false
}
