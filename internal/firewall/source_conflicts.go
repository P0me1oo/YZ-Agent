package firewall

import (
	"context"
	"fmt"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

func (b *systemBackend) checkUFWSourceConflicts(allows []Rule, existing []ufwRule) error {
	for _, wanted := range allows {
		if wanted.Source == "" {
			continue
		}
		for _, current := range existing {
			if !overlaps(wanted, current.Rule) {
				continue
			}
			if !current.ScopedSource && current.Action == "ALLOW" && current.Comment == b.ufwComment(current.Rule) &&
				b.hasOwned(ownedRule{Backend: "ufw", Rule: current.Rule}) {
				continue
			}
			return pending(fmt.Sprintf("落地端口 %s/%s 存在非本实例的 UFW 规则，请确认共用情况；未修改该端口规则", wanted.Ports, wanted.Protocol))
		}
	}
	return nil
}

func hasSources(allows []Rule) bool {
	for _, rule := range allows {
		if rule.Source != "" {
			return true
		}
	}
	return false
}

func (b *systemBackend) checkFirewalldSourceConflicts(ctx context.Context, zone string, allows []Rule, existing map[string]bool) error {
	if !hasSources(allows) {
		return nil
	}
	for text := range existing {
		owned := false
		for _, rule := range b.state.Owned {
			if rule.Backend == "firewalld" && rule.Zone == zone && b.richRule(rule.Rule) == text {
				owned = true
				break
			}
		}
		if owned {
			continue
		}
		// 无端口约束的 rich rule 也可能先于托管规则放行或拒绝，无法确认时不覆盖。
		for _, wanted := range allows {
			if wanted.Source != "" && richRuleMayOverlap(text, wanted) {
				return pending("落地端口所在区域存在其他 firewalld 规则，请确认共用情况；未修改来源规则")
			}
		}
	}
	for _, option := range []string{"--get-target", "--list-ports", "--list-services"} {
		output, err := b.commands.Run(ctx, "firewall-cmd", "", "--zone="+zone, option)
		if err != nil {
			return err
		}
		if option == "--get-target" && strings.TrimSpace(output) == "ACCEPT" {
			return pending("落地区域默认全部放行，请确认 firewalld 区域策略；未修改来源规则")
		}
		if option == "--list-ports" && publicPortsOverlap(output, allows) {
			return pending("落地端口已有 firewalld 公开放行，请确认共用情况；未修改来源规则")
		}
		if option == "--list-services" {
			for _, service := range strings.Fields(output) {
				ports, err := b.commands.Run(ctx, "firewall-cmd", "", "--service="+service, "--get-ports")
				if err != nil {
					return err
				}
				if publicPortsOverlap(ports, allows) {
					return pending("落地端口与 firewalld 服务共用，请确认服务端口；未修改来源规则")
				}
			}
		}
	}
	return nil
}

func publicPortsOverlap(text string, allows []Rule) bool {
	for _, field := range strings.Fields(text) {
		ports, protocol, ok := strings.Cut(field, "/")
		if !ok {
			continue
		}
		ranges, err := portset.Parse(ports)
		if err != nil {
			return true
		}
		for _, rule := range allows {
			for _, ports := range ranges {
				if rule.Source != "" && rule.Protocol == protocol && rule.Ports.Overlaps(ports) {
					return true
				}
			}
		}
	}
	return false
}

func richRuleMayOverlap(text string, wanted Rule) bool {
	otherFamily := 6
	if wanted.Family == 6 {
		otherFamily = 4
	}
	if strings.Contains(text, fmt.Sprintf(`family="ipv%d"`, otherFamily)) {
		return false
	}
	_, tail, hasPort := strings.Cut(text, `port port="`)
	if !hasPort {
		return true
	}
	ports, _, ok := strings.Cut(tail, `"`)
	if !ok {
		return true
	}
	protocol := "tcp"
	if strings.Contains(tail, `protocol="udp"`) {
		protocol = "udp"
	}
	return publicPortsOverlap(ports+"/"+protocol, []Rule{wanted})
}
