// Package portforward manages server-level transports independently of proxy kernels.
package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

type Endpoint struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}
type Rule struct {
	ID         int      `json:"id"`
	Mode       string   `json:"mode"`
	ListenPort int      `json:"listen_port"`
	Target     Endpoint `json:"target"`
	Network    string   `json:"network"`
}
type Policy struct {
	Version int        `json:"version"`
	Rules   []Rule     `json:"rules"`
	Ingress []Endpoint `json:"ingress"`
}
type Status struct {
	ID      int    `json:"id"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
	Warning string `json:"warning,omitempty"`
}
type resolvedRule struct {
	Rule
	IP netip.Addr
}
type lookupFunc func(context.Context, string) ([]netip.Addr, error)

func (p Policy) Validate() error {
	if p.Version != 1 || len(p.Rules) > 1000 || len(p.Ingress) > 20000 {
		return errors.New("unsupported or oversized forwarding policy")
	}
	ids, ports := map[int]bool{}, map[int]bool{}
	for _, r := range p.Rules {
		if r.ID <= 0 || ids[r.ID] || ports[r.ListenPort] || r.ListenPort < 1 || r.ListenPort > 65535 || r.Target.Port < 1 || r.Target.Port > 65535 || !validAddress(r.Target.Address) {
			return errors.New("invalid forwarding identity, address or overlapping port")
		}
		if r.Mode != "realm" && r.Mode != "nftables" {
			return errors.New("unsupported forwarding mode")
		}
		if r.Network != "tcp" && r.Network != "udp" && r.Network != "tcp,udp" {
			return errors.New("unsupported forwarding transport")
		}
		ids[r.ID], ports[r.ListenPort] = true, true
	}
	for _, e := range p.Ingress {
		if !validAddress(e.Address) || e.Port < 1 || e.Port > 65535 {
			return errors.New("invalid forwarding ingress")
		}
	}
	return nil
}
func validAddress(s string) bool {
	if ip, e := netip.ParseAddr(s); e == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
func resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, e := netip.ParseAddr(host); e == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	sort.Slice(ips, func(i, j int) bool {
		if ips[i].Is4() != ips[j].Is4() {
			return ips[i].Is4()
		}
		return ips[i].Less(ips[j])
	})
	if e == nil && len(ips) == 0 {
		e = errors.New("no target addresses")
	}
	return ips, e
}
func resolveRule(ctx context.Context, r Rule, p Policy, local map[netip.Addr]bool, lookup lookupFunc) (resolvedRule, error) {
	ips, err := lookup(ctx, r.Target.Address)
	if err != nil {
		return resolvedRule{}, resolutionError("cannot resolve target address", err)
	}
	if len(ips) == 0 {
		return resolvedRule{}, errors.New("target has no address")
	}
	for _, ip := range ips {
		if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
			return resolvedRule{}, errors.New("invalid target IP")
		}
		if local[ip.Unmap()] || ip.IsLoopback() {
			for _, other := range p.Rules {
				if other.ListenPort == r.Target.Port {
					return resolvedRule{}, errors.New("target loops into a local forwarding listener")
				}
			}
		}
		for _, entry := range p.Ingress {
			if entry.Port != r.Target.Port {
				continue
			}
			addresses, e := lookup(ctx, entry.Address)
			if e != nil {
				return resolvedRule{}, resolutionError("cannot verify forwarding loop: ingress DNS failed", e)
			}
			for _, address := range addresses {
				if ip.Unmap() == address.Unmap() {
					return resolvedRule{}, errors.New("target resolves to a forwarding ingress")
				}
			}
		}
	}
	return resolvedRule{r, ips[0].Unmap()}, nil
}
func checkPort(r Rule) error {
	// Reserve both address families during validation; release before spawning Realm.
	var closers []func() error
	defer func() {
		for _, close := range closers {
			_ = close()
		}
	}()
	for _, proto := range strings.Split(r.Network, ",") {
		for _, family := range []string{"4", "6"} {
			address := fmt.Sprintf("0.0.0.0:%d", r.ListenPort)
			if family == "6" {
				address = fmt.Sprintf("[::]:%d", r.ListenPort)
			}
			if proto == "tcp" {
				c, e := net.Listen("tcp"+family, address)
				if e != nil {
					if family == "6" && ipv6Unavailable(e) {
						continue
					}
					return fmt.Errorf("%s port %d unavailable", proto+family, r.ListenPort)
				}
				closers = append(closers, c.Close)
			} else {
				c, e := net.ListenPacket("udp"+family, address)
				if e != nil {
					if family == "6" && ipv6Unavailable(e) {
						continue
					}
					return fmt.Errorf("%s port %d unavailable", proto+family, r.ListenPort)
				}
				closers = append(closers, c.Close)
			}
		}
	}
	return nil
}
