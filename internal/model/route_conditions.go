package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// RouteProtocolBitTorrent 是面板路由可按协议匹配的 BitTorrent。
// Xray 与 sing-box 的嗅探结果都使用这个名称。
const RouteProtocolBitTorrent = "bittorrent"

// supportedRouteProtocols 列出两种内核都能通过嗅探识别、面板允许选择的协议。
var supportedRouteProtocols = map[string]struct{}{
	RouteProtocolBitTorrent: {},
}

// RouteConditions 是面板路由在目标地址之外的附加条件。
// 不同条件之间必须同时满足；同一条件内的多个值任一命中即可。
type RouteConditions struct {
	Protocols []string
	// Ports 为规范化后的单个端口 "443" 或范围 "6881-6889"。
	Ports []string
	// Networks 为 "tcp"、"udp"；为空表示不限。
	Networks []string
}

// Empty 表示路由没有附加条件，只按目标地址匹配。
func (c RouteConditions) Empty() bool {
	return len(c.Protocols) == 0 && len(c.Ports) == 0 && len(c.Networks) == 0
}

// PanelRouteConditions 校验并规范化面板路由的附加条件。
// 任一值无法识别时返回错误，调用方必须跳过整条路由，不能丢掉条件后扩大匹配范围。
func PanelRouteConditions(route RouteRule) (RouteConditions, error) {
	var conditions RouteConditions
	for _, value := range route.Protocols {
		protocol := strings.ToLower(strings.TrimSpace(value))
		if protocol == "" {
			continue
		}
		if _, ok := supportedRouteProtocols[protocol]; !ok {
			return RouteConditions{}, fmt.Errorf("unsupported protocol %q", value)
		}
		conditions.Protocols = appendUnique(conditions.Protocols, protocol)
	}
	for _, value := range route.Ports {
		port, err := normalizeRoutePort(value)
		if err != nil {
			return RouteConditions{}, err
		}
		if port != "" {
			conditions.Ports = appendUnique(conditions.Ports, port)
		}
	}
	for _, value := range route.Networks {
		network := strings.ToLower(strings.TrimSpace(value))
		if network == "" {
			continue
		}
		if network != "tcp" && network != "udp" {
			return RouteConditions{}, fmt.Errorf("unsupported network %q", value)
		}
		conditions.Networks = appendUnique(conditions.Networks, network)
	}
	return conditions, nil
}

// RouteSniffProtocols 返回有效面板路由需要内核嗅探识别的协议，去重并排序。
// 结果为空时两种内核都不开启嗅探，未使用协议匹配的节点配置保持不变。
func RouteSniffProtocols(routes []RouteRule) []string {
	var protocols []string
	for _, route := range routes {
		conditions, err := PanelRouteConditions(route)
		if err != nil {
			continue
		}
		for _, protocol := range conditions.Protocols {
			protocols = appendUnique(protocols, protocol)
		}
	}
	sort.Strings(protocols)
	return protocols
}

// splitRouteList 把面板或独立配置中逗号分隔的文本拆成列表，空文本返回 nil。
func splitRouteList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// normalizeRoutePort 接受 "443"、"6881-6889" 或 "6881:6889"，统一为连字符范围。
func normalizeRoutePort(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	delimiter := ""
	if strings.Contains(value, "-") {
		delimiter = "-"
	} else if strings.Contains(value, ":") {
		delimiter = ":"
	}
	if delimiter == "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("invalid port %q", value)
		}
		return strconv.Itoa(port), nil
	}
	parts := strings.Split(value, delimiter)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid port range %q", value)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 || start > 65535 {
		return "", fmt.Errorf("invalid port range %q", value)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || end < start || end > 65535 {
		return "", fmt.Errorf("invalid port range %q", value)
	}
	if start == end {
		return strconv.Itoa(start), nil
	}
	return strconv.Itoa(start) + "-" + strconv.Itoa(end), nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
