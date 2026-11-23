package singbox

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/kernel/geodata"
	"github.com/xtls/xray-core/common/platform"
)

// prepareGeoRuleSets 把面板分类引用替换成内联规则集，保留普通路由的条件和动作。
func prepareGeoRuleSets(cfg M, dir string) error {
	route, ok := cfg["route"].(M)
	if !ok {
		return nil
	}
	references := make([]string, 0)
	tags := make(map[string]string)
	walkGeoRules(route["rules"], func(rule M) {
		for _, ref := range geoStringList(rule["rule_set"]) {
			isGeo := strings.HasPrefix(ref, "geosite:") || strings.HasPrefix(ref, "geoip:")
			if !isGeo || tags[ref] != "" {
				continue
			}
			sum := sha256.Sum256([]byte(ref))
			tags[ref] = fmt.Sprintf("yz-geodata-%x", sum[:8])
			references = append(references, ref)
		}
	})
	if len(references) == 0 {
		return nil
	}
	if dir == "" {
		dir = platform.NewEnvFlag(platform.AssetLocation).GetValue(platform.GetExecutableDir)
	}
	data, err := geodata.LoadRules(dir, references)
	if err != nil {
		return fmt.Errorf("prepare sing-box route classifications: %w", err)
	}
	sets := make([]M, 0, len(references))
	occupied := make(map[string]bool)
	for _, set := range geoRuleList(route["rule_set"]) {
		for _, tag := range geoStringList(set["tag"]) {
			occupied[tag] = true
		}
		sets = append(sets, set)
	}
	for _, ref := range references {
		tag := tags[ref]
		if occupied[tag] {
			return fmt.Errorf("custom rule-set tag %q conflicts with generated route data", tag)
		}
		sets = append(sets, M{"type": "inline", "tag": tag, "rules": []M{data[ref]}})
	}
	walkGeoRules(route["rules"], func(rule M) {
		values := geoStringList(rule["rule_set"])
		if len(values) == 0 {
			return
		}
		for i, value := range values {
			if tag := tags[value]; tag != "" {
				values[i] = tag
			}
		}
		rule["rule_set"] = values
	})
	route["rule_set"] = sets
	return nil
}

func geoRuleList(value any) []M {
	switch value := value.(type) {
	case []M:
		return value
	case []any:
		rules := make([]M, 0, len(value))
		for _, entry := range value {
			if rule, ok := entry.(M); ok {
				rules = append(rules, rule)
			}
		}
		return rules
	default:
		return nil
	}
}

func walkGeoRules(value any, visit func(M)) {
	for _, rule := range geoRuleList(value) {
		visit(rule)
		walkGeoRules(rule["rules"], visit)
	}
}

func geoStringList(value any) []string {
	switch value := value.(type) {
	case string:
		return []string{value}
	case []string:
		return append([]string{}, value...)
	case []any:
		result := make([]string, 0, len(value))
		for _, entry := range value {
			if text, ok := entry.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
