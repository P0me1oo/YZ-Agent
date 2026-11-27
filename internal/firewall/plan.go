// Package firewall 管理 Node 创建的放行规则与 Hysteria2 端口转发。
package firewall

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
	"go4.org/netipx"
)

// Rule 只保存地址、协议和端口，不保存节点凭据或完整运行配置。
type Rule struct {
	Family   int           `json:"family"`
	Address  string        `json:"address,omitempty"`
	Source   string        `json:"source,omitempty"`
	Protocol string        `json:"protocol"`
	Ports    portset.Range `json:"ports"`
	Target   int           `json:"target,omitempty"`
}

func (r Rule) valid() bool {
	if (r.Family != 4 && r.Family != 6) || !r.Ports.Valid() ||
		(r.Protocol != "tcp" && r.Protocol != "udp") || r.Target < 0 || r.Target > 65535 {
		return false
	}
	if r.Target != 0 && r.Protocol != "udp" {
		return false
	}
	if r.Address != "" {
		addr, err := netip.ParseAddr(r.Address)
		if err != nil || addr.IsUnspecified() || addr.Is4() != (r.Family == 4) {
			return false
		}
	}
	if r.Source != "" {
		address, err := netip.ParseAddr(r.Source)
		if err != nil || address.Zone() != "" || !address.IsGlobalUnicast() || address.Is4() != (r.Family == 4) || r.Target != 0 {
			return false
		}
	}
	return true
}

func (r Rule) key() string {
	key := fmt.Sprintf("%d/%s/%s/%s/%d", r.Family, r.Address, r.Protocol, r.Ports, r.Target)
	if r.Source != "" {
		key += "/" + r.Source
	}
	return key
}

type Plan struct {
	Ready           bool
	Listeners       []Rule
	Redirects       []Rule
	Restricted      bool
	Blocked         []string
	RelayRestricted bool
	ValidUntil      int64
}

// ConfirmationRequired 表示策略无法安全确定；调用方须保留现有监听和规则。
type ConfirmationRequired struct{ Message string }

func (e *ConfirmationRequired) Error() string { return e.Message }

func pending(message string) error { return &ConfirmationRequired{Message: message} }

func PlanForNode(nc *model.NodeSpec, kernelType string) (Plan, error) {
	if nc == nil {
		return Plan{}, nil
	}
	node := *nc
	if node.IsRelayLanding() {
		node.Protocol = node.Relay.Protocol
		if node.Relay.ListenPort != 0 {
			node.ServerPort = node.Relay.ListenPort
		}
	}
	if node.ServerPort < 1 || node.ServerPort > 65535 {
		return Plan{}, fmt.Errorf("防火墙监听端口必须在 1 到 65535 之间")
	}
	addresses, err := listenerAddresses(node.ListenIP)
	if err != nil {
		return Plan{}, err
	}
	// Go 的 IPv4 通配监听在双栈系统上可能实际创建 IPv6 socket。
	if (node.HasRelayFirewall() || node.SourcePolicy.Enabled()) && strings.TrimSpace(node.ListenIP) == "0.0.0.0" {
		addresses = []Rule{{Family: 4}, {Family: 6}}
	}
	protocols, err := listenerProtocols(&node, kernelType)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Ready: true}
	if nc.SourcePolicy.Enabled() {
		if err := nc.SourcePolicy.Validate(); err != nil {
			return Plan{}, err
		}
		if !nc.SourcePolicyReady {
			return Plan{}, fmt.Errorf("系统防火墙来源集合尚未准备完成")
		}
		plan.Restricted = true
		for _, raw := range nc.SourceBlockCIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix.Addr().Zone() != "" || prefix.Addr().Is4In6() {
				return Plan{}, fmt.Errorf("系统防火墙来源集合包含无效网段")
			}
			plan.Blocked = append(plan.Blocked, prefix.Masked().String())
		}
	}
	// The firewall sees the transport peer; the core still evaluates the original
	// source policy against the client address supplied by a trusted proxy.
	if plan.Restricted && nc.AccessPolicyVersion == 1 && nc.GetProxyProtocol() {
		var blocked netipx.IPSetBuilder
		for _, raw := range plan.Blocked {
			blocked.AddPrefix(netip.MustParsePrefix(raw))
		}
		for _, raw := range nc.TrustedProxyIPs {
			if ip, err := netip.ParseAddr(raw); err == nil {
				blocked.Remove(ip.Unmap())
			} else if p, err := netip.ParsePrefix(raw); err == nil {
				blocked.RemovePrefix(p.Masked())
			} else {
				return Plan{}, fmt.Errorf("可信代理来源无效")
			}
		}
		set, err := blocked.IPSet()
		if err != nil {
			return Plan{}, err
		}
		plan.Blocked = nil
		for _, p := range set.Prefixes() {
			plan.Blocked = append(plan.Blocked, p.String())
		}
	}
	for _, address := range addresses {
		for _, protocol := range protocols {
			rule := address
			rule.Protocol = protocol
			rule.Ports = portset.Range{From: node.ServerPort, To: node.ServerPort}
			plan.Listeners = append(plan.Listeners, rule)
		}
	}
	if node.HasRelayFirewall() {
		plan.RelayRestricted = true
		plan.Restricted = true
		policy := node.Relay.Firewall
		if policy.Version != 1 || policy.Status != "ready" || policy.ValidUntil <= time.Now().Unix() || len(policy.Sources) == 0 {
			if policy.Status == "pending" && policy.Message != "" && len(policy.Message) <= 1000 {
				return plan, pending(policy.Message)
			}
			return plan, pending("前置出口策略未完成核对或已过期；保留原防火墙规则")
		}
		if len(policy.Sources) > 4096 {
			return plan, pending("前置来源名单超过容量；保留原规则")
		}
		var blocked netipx.IPSetBuilder
		blocked.AddPrefix(netip.MustParsePrefix("0.0.0.0/0"))
		blocked.AddPrefix(netip.MustParsePrefix("::/0"))
		for _, raw := range policy.Sources {
			ip, err := netip.ParseAddr(raw)
			if err != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" || ip.Is4In6() {
				return plan, pending("前置出口不是有效的具体公网 IP；保留原规则")
			}
			blocked.Remove(ip)
		}
		// 与节点手工设置的来源限制取交集，不扩大手工许可。
		for _, raw := range plan.Blocked {
			blocked.AddPrefix(netip.MustParsePrefix(raw))
		}
		set, err := blocked.IPSet()
		if err != nil {
			return plan, err
		}
		plan.Blocked = []string{}
		for _, prefix := range set.Prefixes() {
			plan.Blocked = append(plan.Blocked, prefix.String())
		}
		plan.ValidUntil = policy.ValidUntil
	}
	if strings.TrimSpace(node.PortHopping) == "" {
		return plan, nil
	}
	if node.Protocol != "hysteria" || node.Version != 2 || node.IsRelayLanding() {
		return Plan{}, fmt.Errorf("端口跳跃只支持 Hysteria2 普通节点或中转入口")
	}
	ports, err := portset.Parse(node.PortHopping)
	if err != nil {
		return Plan{}, fmt.Errorf("port_hopping: %w", err)
	}
	for _, address := range addresses {
		for _, ports := range portset.Without(ports, node.ServerPort) {
			rule := address
			rule.Protocol, rule.Ports, rule.Target = "udp", ports, node.ServerPort
			plan.Redirects = append(plan.Redirects, rule)
		}
	}
	return plan, nil
}

func listenerAddresses(raw string) ([]Rule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "::" || raw == "[::]" {
		return []Rule{{Family: 4}, {Family: 6}}, nil
	}
	address, err := netip.ParseAddr(strings.Trim(raw, "[]"))
	if err != nil || address.Zone() != "" {
		return nil, fmt.Errorf("防火墙托管需要有效的监听 IP 地址")
	}
	address = address.Unmap()
	family := 6
	if address.Is4() {
		family = 4
	}
	value := address.String()
	if address.IsUnspecified() {
		value = ""
	}
	return []Rule{{Family: family, Address: value}}, nil
}

func listenerProtocols(node *model.NodeSpec, kernelType string) ([]string, error) {
	if node.UsesXHTTP3() {
		return []string{"udp"}, nil
	}
	switch node.Protocol {
	case "balancer":
		if node.Balancer != nil && node.Balancer.Network == "tcp" {
			return []string{node.Balancer.Network}, nil
		}
		return nil, fmt.Errorf("调度入口缺少传输协议")
	case "hysteria", "tuic", "wireguard":
		return []string{"udp"}, nil
	case "shadowsocks", "naive":
		return []string{"tcp", "udp"}, nil
	case "socks":
		if kernelType == "xray" {
			return []string{"tcp", "udp"}, nil
		}
		// sing-box 的 SOCKS UDP 会话动态分配端口，此处只管理固定监听。
		return []string{"tcp"}, nil
	case "http", "anytls":
		return []string{"tcp"}, nil
	case "mieru":
		if strings.EqualFold(node.Transport, "UDP") {
			return []string{"udp"}, nil
		}
		return []string{"tcp"}, nil
	case "vmess", "vless", "trojan":
		switch strings.ToLower(node.Network) {
		case "kcp", "mkcp", "quic", "hysteria":
			return []string{"udp"}, nil
		default:
			return []string{"tcp"}, nil
		}
	default:
		return nil, fmt.Errorf("不支持为协议 %q 自动计算防火墙端口", node.Protocol)
	}
}

// combinePlans 从仍在运行的节点计算规则并集，重复同步和共享端口不会累加规则。
func combinePlans(plans map[string]Plan) (allows, redirects []Rule, err error) {
	// 不同节点不能通过取来源并集来扩大落地权限，也不能收紧共用的公开端口。
	for owner, left := range plans {
		for other, right := range plans {
			if owner >= other {
				continue
			}
			for _, a := range append(append([]Rule(nil), left.Listeners...), left.Redirects...) {
				for _, b := range append(append([]Rule(nil), right.Listeners...), right.Redirects...) {
					if overlaps(a, b) && (left.Restricted || right.Restricted || a.Source != "" || b.Source != "") {
						return nil, nil, pending(fmt.Sprintf("落地端口 %s/%s 被多个节点共用，请确认并改用独立端口；保留原防火墙规则", a.Ports, a.Protocol))
					}
				}
			}
		}
	}
	var claims []Rule
	for _, plan := range plans {
		for _, listener := range plan.Listeners {
			allows = append(allows, listener)
			if listener.Protocol == "udp" {
				listener.Target = listener.Ports.From
				claims = append(claims, listener)
			}
		}
		for _, redirect := range plan.Redirects {
			redirects = append(redirects, redirect)
			claims = append(claims, redirect)
			redirect.Target = 0
			allows = append(allows, redirect)
		}
	}
	for i, left := range claims {
		for _, right := range claims[i+1:] {
			if left.Family == right.Family && (left.Address == "" || right.Address == "" || left.Address == right.Address) &&
				left.Ports.Overlaps(right.Ports) && left.Target != right.Target {
				return nil, nil, fmt.Errorf("UDP 端口范围 %s 与 %s 冲突，不能转发到不同监听端口", left.Ports, right.Ports)
			}
		}
	}
	return mergeRules(allows), mergeRules(redirects), nil
}

func mergeRules(rules []Rule) []Rule {
	type group struct {
		rule  Rule
		ports []portset.Range
	}
	groups := map[string]*group{}
	for _, rule := range rules {
		key := fmt.Sprintf("%d/%s/%s/%d/%s", rule.Family, rule.Address, rule.Protocol, rule.Target, rule.Source)
		if groups[key] == nil {
			groups[key] = &group{rule: rule}
		}
		groups[key].ports = append(groups[key].ports, rule.Ports)
	}
	var merged []Rule
	for _, group := range groups {
		for _, ports := range portset.Merge(group.ports) {
			rule := group.rule
			rule.Ports = ports
			merged = append(merged, rule)
		}
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].key() < merged[j].key() })
	return merged
}

func overlaps(a, b Rule) bool {
	return a.Family == b.Family && a.Protocol == b.Protocol &&
		(a.Address == "" || b.Address == "" || a.Address == b.Address) && a.Ports.Overlaps(b.Ports)
}
