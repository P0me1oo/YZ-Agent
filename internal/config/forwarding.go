package config

import (
	"fmt"
	"net/netip"
)

// ForwardingConfig is a local authorization boundary, never populated from panel policy.
type ForwardingConfig struct {
	AllowedTargets []ForwardingTarget `yaml:"allowed_targets,omitempty"`
}

// An explicit empty list revokes inherited permissions and must survive
// privacy wrapping, export and later YAML rewrites.
func (c ForwardingConfig) IsZero() bool { return c.AllowedTargets == nil }

func (c ForwardingConfig) MarshalYAML() (any, error) {
	if c.AllowedTargets == nil {
		return map[string]any{}, nil
	}
	return map[string]any{"allowed_targets": c.AllowedTargets}, nil
}

type ForwardingTarget struct {
	CIDR  string `yaml:"cidr"`
	Ports []int  `yaml:"ports"`
}

func (c *ForwardingConfig) inheritFrom(parent ForwardingConfig) {
	if c.AllowedTargets == nil && parent.AllowedTargets != nil {
		c.AllowedTargets = make([]ForwardingTarget, len(parent.AllowedTargets))
		for i, target := range parent.AllowedTargets {
			c.AllowedTargets[i] = ForwardingTarget{CIDR: target.CIDR, Ports: append([]int{}, target.Ports...)}
		}
	}
}

func ValidateForwarding(c ForwardingConfig) error {
	if len(c.AllowedTargets) > 256 {
		return fmt.Errorf("forwarding.allowed_targets 最多允许 256 条本机授权")
	}
	for _, target := range c.AllowedTargets {
		prefix, err := netip.ParsePrefix(target.CIDR)
		if err != nil || prefix.Addr().Is4In6() || prefix != prefix.Masked() {
			return fmt.Errorf("forwarding.allowed_targets.cidr 必须为规范的 IPv4 或 IPv6 网段")
		}
		if len(target.Ports) == 0 || len(target.Ports) > 256 {
			return fmt.Errorf("forwarding.allowed_targets.ports 必须明确列出 1 至 256 个端口")
		}
		seen := map[int]bool{}
		for _, port := range target.Ports {
			if port < 1 || port > 65535 || seen[port] {
				return fmt.Errorf("forwarding.allowed_targets.ports 包含无效或重复端口")
			}
			seen[port] = true
		}
	}
	return nil
}
