package service

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type relayEgress struct {
	Version   int                `json:"version"`
	CheckedAt int64              `json:"checked_at"`
	Children  []relayEgressChild `json:"children"`
}

type relayEgressChild struct {
	NodeID   int      `json:"node_id"`
	Token    string   `json:"token"`
	Complete bool     `json:"complete"`
	Sources  []string `json:"sources"`
}

// UDP connect 只读取选路，不发送数据；NAT 后地址仍由面板的认证来源交叉核对。
func relayRouteSource(ctx context.Context, destination netip.Addr, port int) (string, error) {
	network := "udp6"
	if destination.Is4() {
		network = "udp4"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(destination.String(), strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap().String(), nil
}

type relayLookup func(context.Context, string, string) ([]netip.Addr, error)
type relayRoute func(context.Context, netip.Addr, int) (string, error)

func collectRelayEgress(ctx context.Context, node *model.NodeSpec, lookup relayLookup, route relayRoute) *relayEgress {
	if node == nil || len(node.RelayEgressTargets) == 0 {
		return nil
	}
	report := &relayEgress{Version: 1, CheckedAt: time.Now().Unix(), Children: []relayEgressChild{}}
	if len(node.RelayEgressTargets) > 1000 {
		return report
	}
	for _, target := range node.RelayEgressTargets {
		item := relayEgressChild{NodeID: target.NodeID, Token: target.Token, Sources: []string{}}
		defaultRoute := len(node.CustomOutbounds)+len(node.CustomRoutes)+len(node.CustomRouteRules) == 0
		// Next namespaced exits cannot override a relay transport; relay selection
		// precedes non-block panel routing and has no outbound chaining.
		if node.AccessPolicyVersion == 1 && len(node.CustomRoutes)+len(node.CustomRouteRules) == 0 {
			defaultRoute = true
			for _, out := range node.CustomOutbounds {
				if !strings.HasPrefix(out.Tag, "next-exit-") || out.ProxyTag != "" {
					defaultRoute = false
					break
				}
			}
		}
		valid := target.NodeID > 0 && target.Port > 0 && target.Port <= 65535 && len(target.Token) == 64
		if valid && defaultRoute {
			addresses, err := lookup(ctx, "ip", target.Address)
			item.Complete = err == nil && len(addresses) > 0 && len(addresses) <= 32
			if item.Complete {
				for _, address := range addresses {
					address = address.Unmap()
					if !address.IsGlobalUnicast() || address.IsPrivate() || address.Zone() != "" {
						item.Complete = false
						break
					}
					source, err := route(ctx, address, target.Port)
					ip, parseErr := netip.ParseAddr(source)
					if err != nil || parseErr != nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" || ip.Unmap().Is4() != address.Is4() {
						item.Complete = false
						break
					}
					item.Sources = append(item.Sources, ip.Unmap().String())
				}
			}
		}
		if !item.Complete {
			item.Sources = []string{}
		}
		slices.Sort(item.Sources)
		item.Sources = slices.Compact(item.Sources)
		report.Children = append(report.Children, item)
	}
	return report
}

func (s *Service) relayEgressLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.metricsMu.RLock()
		node := s.lastConfig
		s.metricsMu.RUnlock()
		checkCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		s.relayEgress.Store(collectRelayEgress(checkCtx, node, net.DefaultResolver.LookupNetIP, relayRouteSource))
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
