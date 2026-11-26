package firewall

import (
	"context"
	"fmt"
	"sort"
)

func (b *systemBackend) applyRedirects(ctx context.Context, rules []Rule) error {
	driver := b.state.Redirect
	if len(rules) > 0 {
		driver = b.cfg.RedirectBackend
		if driver == "" || driver == "auto" {
			switch {
			case b.commands.Available("nft"):
				driver = "nftables"
			case b.commands.Available("iptables"):
				driver = "iptables"
			default:
				return fmt.Errorf("端口跳跃需要 nftables 或 iptables/ip6tables")
			}
		}
	}
	if driver == "" {
		return nil
	}
	if previous := b.state.Redirect; previous != "" && previous != driver {
		if err := b.syncRedirectDriver(ctx, previous, nil); err != nil {
			return err
		}
	}
	if b.state.Redirect != driver {
		b.state.Redirect = driver
	}
	if driver == "iptables" {
		families := map[int]bool{}
		for _, family := range b.state.RedirectFamilies {
			if family == 6 && b.ipv6Unavailable {
				continue
			}
			families[family] = true
		}
		for _, rule := range rules {
			families[rule.Family] = true
		}
		b.state.RedirectFamilies = nil
		for family := range families {
			b.state.RedirectFamilies = append(b.state.RedirectFamilies, family)
		}
		sort.Ints(b.state.RedirectFamilies)
	}
	if err := b.save(); err != nil {
		return err
	}
	if err := b.syncRedirectDriver(ctx, driver, rules); err != nil {
		return err
	}
	if len(rules) == 0 {
		b.state.Redirect, b.state.RedirectFamilies = "", nil
		return b.save()
	}
	return nil
}

func (b *systemBackend) syncRedirectDriver(ctx context.Context, driver string, rules []Rule) error {
	switch driver {
	case "nftables":
		return b.syncNFT(ctx, rules)
	case "iptables":
		return b.syncIPTables(ctx, rules)
	default:
		return fmt.Errorf("未知端口转发后端")
	}
}
