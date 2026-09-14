package singbox

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// 只翻译有明确对应关系的旧参数；优先级、回落和其他原生选项仍由对应内核解释。
// m 是生成器新建的出站对象，不能修改面板持有的 settings 或其中的嵌套对象。
func convertLegacyDirectOptions(m M) error {
	source, err := legacyDirectSource(m)
	if err != nil {
		return err
	}
	strategy := ""
	if raw, exists := m["domainStrategy"]; exists {
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("settings.domainStrategy must be a string")
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "asis":
			strategy = "as_is"
		case "forceipv4":
			strategy = "ipv4_only"
		case "forceipv6":
			strategy = "ipv6_only"
		default:
			return fmt.Errorf("settings.domainStrategy: sing-box compatibility supports AsIs, ForceIPv4 and ForceIPv6; use native sing-box settings for other strategies")
		}
	}
	if source.IsValid() {
		bindField, familyStrategy, otherBind := "inet6_bind_address", "ipv6_only", "inet4_bind_address"
		if source.Is4() {
			bindField, familyStrategy, otherBind = "inet4_bind_address", "ipv4_only", "inet6_bind_address"
		}
		if strategy != "" && strategy != "as_is" && strategy != familyStrategy {
			return fmt.Errorf("settings.domainStrategy conflicts with the send_through address family")
		}
		// Xray 的单地址绑定只能使用这一地址族，解析时也必须保留此约束。
		strategy = familyStrategy
		if value, exists := m[otherBind]; exists && value != nil {
			return fmt.Errorf("settings.send_through conflicts with settings.%s", otherBind)
		}
		if value, exists := m[bindField]; exists && value != nil {
			text, ok := value.(string)
			address, parseErr := netip.ParseAddr(strings.TrimSpace(text))
			if !ok || parseErr != nil || address.Unmap() != source {
				return fmt.Errorf("settings.send_through conflicts with settings.%s", bindField)
			}
		}
		m[bindField] = source.String()
	}
	if strategy != "" {
		if native, exists := m["domain_strategy"]; exists && native != nil && native != strategy {
			return fmt.Errorf("legacy direct strategy conflicts with settings.domain_strategy")
		}
		if resolver, ok := m["domain_resolver"].(map[string]any); ok {
			if native, exists := resolver["strategy"]; exists && native != nil && native != "" && native != "as_is" && native != strategy {
				return fmt.Errorf("legacy direct strategy conflicts with settings.domain_resolver.strategy")
			}
		}
		// 当前固定版本仍支持直连的 domain_strategy，沿用原生 DNS 选择和配置。
		m["domain_strategy"] = strategy
	}
	delete(m, "domainStrategy")
	for _, key := range model.SendThroughKeys {
		delete(m, key)
	}
	return nil
}

func legacyDirectSource(settings map[string]any) (netip.Addr, error) {
	var source netip.Addr
	for _, key := range model.SendThroughKeys {
		raw, exists := settings[key]
		if !exists {
			continue
		}
		value, ok := raw.(string)
		address, err := netip.ParseAddr(strings.TrimSpace(value))
		address = address.Unmap()
		if !ok || err != nil || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" {
			return netip.Addr{}, fmt.Errorf("settings.%s must be a concrete IPv4 or IPv6 source address", key)
		}
		if source.IsValid() && source != address {
			return netip.Addr{}, fmt.Errorf("settings.send_through conflicts with settings.sendThrough")
		}
		source = address
	}
	return source, nil
}
