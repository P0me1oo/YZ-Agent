//go:build linux

package portforward

import (
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type forwardingFlowFilter struct {
	rule  resolvedRule
	local map[netip.Addr]bool
}

func (f forwardingFlowFilter) MatchConntrackFlow(flow *netlink.ConntrackFlow) bool {
	original, ok := netip.AddrFromSlice(flow.Forward.DstIP)
	if !ok || !f.local[original.Unmap()] {
		return false
	}
	target, ok := netip.AddrFromSlice(flow.Reverse.SrcIP)
	if !ok || target.Unmap() != f.rule.IP.Unmap() {
		return false
	}
	protocol := "tcp"
	if flow.Forward.Protocol == unix.IPPROTO_UDP {
		protocol = "udp"
	} else if flow.Forward.Protocol != unix.IPPROTO_TCP {
		return false
	}
	return strings.Contains(f.rule.Network, protocol) && int(flow.Forward.DstPort) == f.rule.ListenPort && int(flow.Reverse.SrcPort) == f.rule.Target.Port
}
func (m *Manager) previousNFTRules() ([]resolvedRule, error) {
	raw, err := os.ReadFile(filepath.Join(m.dir, "nft-active.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rules []resolvedRule
	if len(raw) > 4<<20 || json.Unmarshal(raw, &rules) != nil {
		return nil, errors.New("invalid persisted NAT ownership")
	}
	p := Policy{Version: 1}
	for _, r := range rules {
		if !r.IP.IsValid() {
			return nil, errors.New("invalid persisted NAT address")
		}
		p.Rules = append(p.Rules, r.Rule)
	}
	// The journal may contain both old and new versions of a rule during a transaction.
	for _, r := range p.Rules {
		if e := (Policy{Version: 1, Rules: []Rule{r}}).Validate(); e != nil {
			return nil, e
		}
	}
	return rules, nil
}
func (m *Manager) saveNFTRules(rules []resolvedRule) error {
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(m.dir, "nft-active.json"), raw)
}
func clearForwardingFlows(previous, current []resolvedRule) error {
	var removed []resolvedRule
	for _, old := range previous {
		if !slices.Contains(current, old) {
			removed = append(removed, old)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return err
	}
	local := map[netip.Addr]bool{}
	for _, a := range addresses {
		if p, e := netip.ParsePrefix(a.String()); e == nil {
			local[p.Addr().Unmap()] = true
		}
	}
	handle, err := netlink.NewHandle(unix.NETLINK_NETFILTER)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err = handle.SetSocketTimeout(2 * time.Second); err != nil {
		return err
	}
	for _, family := range []netlink.InetFamily{unix.AF_INET, unix.AF_INET6} {
		var filters []netlink.CustomConntrackFilter
		for _, r := range removed {
			if r.IP.Is4() == (family == unix.AF_INET) {
				filters = append(filters, forwardingFlowFilter{r, local})
			}
		}
		if len(filters) == 0 {
			continue
		}
		if _, err = handle.ConntrackDeleteFilters(netlink.ConntrackTable, family, filters...); err != nil {
			return errors.New("cannot clear previous forwarding connections")
		}
	}
	return nil
}
