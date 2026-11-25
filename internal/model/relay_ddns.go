package model

import (
	"fmt"
	"net/netip"
)

// ConnectAddress 使用面板已完成 DNS 写入的 IP，避免长连接和 WG 固定在旧解析。
// Address 保留落地域名，TLS 身份继续使用独立的握手配置。
func (c RelayChild) ConnectAddress() string {
	if c.ResolvedAddress != "" {
		return c.ResolvedAddress
	}
	return c.Address
}

func (c RelayChild) validateResolvedAddress() error {
	if c.ResolvedAddress == "" {
		return nil
	}
	address, err := netip.ParseAddr(c.ResolvedAddress)
	if err != nil || address.IsUnspecified() || address.IsMulticast() || address.Zone() != "" {
		return fmt.Errorf("resolved_address must be a unicast IP")
	}
	return nil
}
