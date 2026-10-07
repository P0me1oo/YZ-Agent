// Package sourcepolicy 提供两种核心共用的大陆来源网段与例外地址处理。
package sourcepolicy

import (
	"fmt"
	"net/netip"
	"strings"

	"go4.org/netipx"
)

type Policy struct {
	BlockCN  bool     `json:"block_cn"`
	AllowIPs []string `json:"allow_ips,omitempty"`
}

func Clone(p *Policy) *Policy {
	if p == nil {
		return nil
	}
	return &Policy{BlockCN: p.BlockCN, AllowIPs: append([]string(nil), p.AllowIPs...)}
}

func (p *Policy) Enabled() bool { return p != nil && p.BlockCN }

func (p *Policy) Validate() error {
	if p == nil {
		return nil
	}
	if len(p.AllowIPs) > 128 {
		return fmt.Errorf("来源例外最多允许 128 个 IP")
	}
	for _, value := range p.AllowIPs {
		ip, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil || ip.Zone() != "" || ip.Unmap().IsUnspecified() || ip.Unmap().IsMulticast() {
			return fmt.Errorf("来源例外必须填写具体的 IPv4 或 IPv6 地址")
		}
	}
	return nil
}

// Blocked 从大陆集合中扣除例外，例外连接仍继续走原有路由和权限检查。
func (p *Policy) Blocked(snapshot Snapshot) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !p.Enabled() {
		return nil, nil
	}
	if snapshot.set == nil {
		return nil, fmt.Errorf("大陆来源网段库尚未就绪")
	}
	var builder netipx.IPSetBuilder
	builder.AddSet(snapshot.set)
	for _, value := range p.AllowIPs {
		ip, _ := netip.ParseAddr(strings.TrimSpace(value))
		builder.Remove(ip.Unmap())
	}
	set, err := builder.IPSet()
	if err != nil {
		return nil, fmt.Errorf("生成大陆来源拦截网段失败: %w", err)
	}
	result := make([]string, 0, len(set.Prefixes()))
	for _, prefix := range set.Prefixes() {
		result = append(result, prefix.String())
	}
	return result, nil
}
