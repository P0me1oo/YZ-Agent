package model

import (
	"encoding/base64"
	"fmt"
	"net/netip"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func (n *NodeSpec) IsDirectWireGuard() bool {
	return n != nil && n.Protocol == "wireguard" && !n.IsRelayLanding()
}

func CloneWireGuardConfig(v *panel.WireGuardConfig) *panel.WireGuardConfig {
	if v == nil {
		return nil
	}
	c := *v
	c.Address = append([]string(nil), v.Address...)
	return &c
}

func CloneWireGuardPeer(v *panel.WireGuardPeer) *panel.WireGuardPeer {
	if v == nil {
		return nil
	}
	c := *v
	c.Address = append([]string(nil), v.Address...)
	return &c
}

func ValidateWireGuardKey(value string) error {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 {
		return fmt.Errorf("WireGuard 密钥格式无效")
	}
	var nonzero byte
	for _, v := range b {
		nonzero |= v
	}
	if nonzero == 0 {
		return fmt.Errorf("WireGuard 密钥不可全为零")
	}
	return nil
}

func ValidateWireGuardAddresses(values []string) error {
	if len(values) == 0 || len(values) > 2 {
		return fmt.Errorf("WireGuard 需要一到两个隧道地址")
	}
	seen := make(map[bool]bool)
	for _, v := range values {
		p, err := netip.ParsePrefix(v)
		if err != nil || !p.IsSingleIP() || !p.Addr().IsPrivate() || p.Addr().Is4In6() || seen[p.Addr().Is4()] {
			return fmt.Errorf("WireGuard 隧道地址必须为独立的内网 IPv4/IPv6 地址")
		}
		seen[p.Addr().Is4()] = true
	}
	return nil
}

func ValidateDirectWireGuard(n *NodeSpec) error {
	if !n.IsDirectWireGuard() {
		return nil
	}
	if n.WireGuard == nil {
		return fmt.Errorf("普通 WireGuard 节点缺少服务端身份")
	}
	if err := ValidateWireGuardKey(n.WireGuard.PrivateKey); err != nil {
		return err
	}
	if err := ValidateWireGuardAddresses(n.WireGuard.Address); err != nil {
		return err
	}
	if n.WireGuard.MTU < 1280 || n.WireGuard.MTU > 1500 {
		return fmt.Errorf("WireGuard MTU 必须在 1280–1500 之间")
	}
	return nil
}
