package kernel

import (
	"net/netip"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// BlockRoutesNeedIP 表示有效的面板拦截需要检查目标 IP，域名目标也需要先解析。
func BlockRoutesNeedIP(routes []model.RouteRule) bool {
	return routesNeedIP(routes, true)
}

func RoutesNeedIP(routes []model.RouteRule) bool { return routesNeedIP(routes, false) }

func routesNeedIP(routes []model.RouteRule, blocksOnly bool) bool {
	for _, route := range routes {
		if blocksOnly && route.Action != "block" {
			continue
		}
		if _, err := model.PanelRouteConditions(route); err != nil {
			continue
		}
		for _, value := range route.Match {
			value = strings.TrimSpace(value)
			if strings.HasPrefix(value, "geoip:") || strings.Contains(value, "/") {
				return true
			}
			if _, err := netip.ParseAddr(value); err == nil {
				return true
			}
		}
	}
	return false
}

// NeedsGeoIP returns true when any panel route rule contains a "geoip:" match entry.
func NeedsGeoIP(routes []model.RouteRule) bool {
	for _, r := range routes {
		for _, m := range r.Match {
			if strings.HasPrefix(m, "geoip:") {
				return true
			}
		}
	}
	return false
}

// NeedsGeoSite returns true when any panel route rule contains a "geosite:" match entry.
func NeedsGeoSite(routes []model.RouteRule) bool {
	for _, r := range routes {
		for _, m := range r.Match {
			if strings.HasPrefix(m, "geosite:") {
				return true
			}
		}
	}
	return false
}
