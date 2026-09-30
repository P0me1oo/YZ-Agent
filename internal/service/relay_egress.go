package service

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type relayEgress struct {
	CheckedAt int64              `json:"checked_at"`
	Children  []relayEgressChild `json:"children"`
}

type relayEgressChild struct {
	NodeID  int      `json:"node_id"`
	Address string   `json:"address"`
	Port    int      `json:"port"`
	Sources []string `json:"sources"`
}

// 读取内置中转目标实际选用的系统源地址，不发送探测包，不打开额外端口。
// 面板还要和经过认证的公网地址回报核对；NAT、私网或不同出口不能靠域名猜测。
func relayRouteSource(ctx context.Context, destination netip.Addr, port int) (string, error) {
	network := "udp6"
	if destination.Is4() {
		network = "udp4"
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(destination.String(), strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	defer connection.Close()
	return connection.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap().String(), nil
}

func collectRelayEgress(ctx context.Context, node *model.NodeSpec, lookup func(context.Context, string, string) ([]netip.Addr, error), route func(context.Context, netip.Addr, int) (string, error)) *relayEgress {
	if !node.IsRelayEntry() {
		return nil
	}
	report := &relayEgress{CheckedAt: time.Now().Unix(), Children: []relayEgressChild{}}
	for _, child := range node.Relay.Children {
		item := relayEgressChild{NodeID: child.NodeID, Address: child.Address, Port: child.Port, Sources: []string{}}
		// 自定义出站可能改变系统选路，自动确认仅覆盖默认内置中转路径。
		if len(node.CustomOutbounds)+len(node.CustomRoutes)+len(node.CustomRouteRules) == 0 {
			addresses, err := lookup(ctx, "ip", child.Address)
			if err == nil {
				seen := map[string]bool{}
				for _, address := range addresses {
					source, err := route(ctx, address.Unmap(), child.Port)
					if err == nil && !seen[source] {
						seen[source] = true
						item.Sources = append(item.Sources, source)
					}
				}
			}
		}
		sort.Strings(item.Sources)
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
