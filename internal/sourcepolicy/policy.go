// Package sourcepolicy 提供两种核心共用的来源黑白名单处理。
package sourcepolicy

import (
	"fmt"
	"net/netip"
	"strings"

	"go4.org/netipx"
)

type Policy struct {
	Mode     string   `json:"mode,omitempty"`
	BlockCN  bool     `json:"block_cn"`
	AllowIPs []string `json:"allow_ips,omitempty"`
	BlockIPs []string `json:"block_ips,omitempty"`
}

func Clone(p *Policy) *Policy {
	if p == nil {
		return nil
	}
	return &Policy{Mode: p.Mode, BlockCN: p.BlockCN, AllowIPs: append([]string(nil), p.AllowIPs...), BlockIPs: append([]string(nil), p.BlockIPs...)}
}

func (p *Policy) Enabled() bool {
	return p != nil && (p.Mode == "blacklist" || p.Mode == "whitelist" || (p.Mode == "" && p.BlockCN))
}

func (p *Policy) NeedsCN() bool { return p.Enabled() && p.Mode != "whitelist" && p.BlockCN }

func (p *Policy) Validate() error {
	if p == nil {
		return nil
	}
	if p.Mode != "" && p.Mode != "disabled" && p.Mode != "blacklist" && p.Mode != "whitelist" {
		return fmt.Errorf("来源限制模式无效")
	}
	if p.Mode == "whitelist" && len(p.AllowIPs) == 0 {
		return fmt.Errorf("白名单至少填写一个 IP")
	}
	for _, list := range [][]string{p.AllowIPs, p.BlockIPs} {
		if len(list) > 128 {
			return fmt.Errorf("每份来源名单最多允许 128 个 IP")
		}
		seen := make(map[netip.Addr]bool)
		for _, value := range list {
			ip, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil || ip.Zone() != "" || ip.Unmap().IsUnspecified() || ip.Unmap().IsMulticast() {
				return fmt.Errorf("来源名单必须填写具体的 IPv4 或 IPv6 地址")
			}
			ip = ip.Unmap()
			if seen[ip] {
				return fmt.Errorf("来源名单不能包含重复 IP")
			}
			seen[ip] = true
		}
	}
	return nil
}

// Blocked 生成统一的拦截集合，放行的连接仍继续走原有路由和权限检查。
func (p *Policy) Blocked(snapshot Snapshot) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !p.Enabled() {
		return nil, nil
	}
	if p.NeedsCN() && snapshot.set == nil {
		return nil, fmt.Errorf("大陆来源网段库尚未就绪")
	}
	var builder netipx.IPSetBuilder
	if p.Mode == "whitelist" {
		builder.AddPrefix(netip.MustParsePrefix("0.0.0.0/0"))
		builder.AddPrefix(netip.MustParsePrefix("::/0"))
	} else if p.NeedsCN() {
		builder.AddSet(snapshot.set)
	}
	for _, value := range p.AllowIPs {
		ip, _ := netip.ParseAddr(strings.TrimSpace(value))
		builder.Remove(ip.Unmap())
	}
	// 黑名单的显式地址优先于地区例外；白名单只按允许地址匹配。
	if p.Mode == "blacklist" {
		for _, value := range p.BlockIPs {
			ip, _ := netip.ParseAddr(strings.TrimSpace(value))
			builder.Add(ip.Unmap())
		}
	}
	set, err := builder.IPSet()
	if err != nil {
		return nil, fmt.Errorf("生成来源拦截网段失败: %w", err)
	}
	result := make([]string, 0, len(set.Prefixes()))
	for _, prefix := range set.Prefixes() {
		result = append(result, prefix.String())
	}
	return result, nil
}
