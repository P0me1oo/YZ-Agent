package portforward

import (
	"errors"
	"net/netip"
	"slices"
)

// TargetPermission is supplied by local configuration, independently of panel rules.
type TargetPermission struct {
	Prefix netip.Prefix
	Ports  []int
}

func cloneTargetPermissions(targets []TargetPermission) []TargetPermission {
	out := make([]TargetPermission, len(targets))
	for i, target := range targets {
		out[i] = TargetPermission{Prefix: target.Prefix, Ports: slices.Clone(target.Ports)}
	}
	return out
}

// IPv6 transition prefixes are denied as well as ordinary private networks: a
// public-looking IPv6 address must not tunnel into an otherwise denied IPv4 target.
var restrictedTargets = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func (m *Manager) allowsTarget(ip netip.Addr, port int, local map[netip.Addr]bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	for _, permission := range m.targets {
		if permission.Prefix.Contains(ip) && slices.Contains(permission.Ports, port) {
			return true
		}
	}
	if local[ip] || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range restrictedTargets {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func validateTargetPermissions(targets []TargetPermission) error {
	for _, target := range targets {
		if !target.Prefix.IsValid() || target.Prefix.Addr().Is4In6() || target.Prefix != target.Prefix.Masked() || len(target.Ports) == 0 {
			return errors.New("invalid local forwarding target permission")
		}
		for _, port := range target.Ports {
			if port < 1 || port > 65535 {
				return errors.New("invalid local forwarding target port")
			}
		}
	}
	return nil
}
