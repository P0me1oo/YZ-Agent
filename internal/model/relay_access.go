package model

import (
	"encoding/json"
	"slices"
)

// 保留 nil 与空列表的区别，避免复制时把拒绝全部变成旧面板兼容模式。
func CloneRelayRoutes(routes []int) []int {
	if routes == nil {
		return nil
	}
	return append([]int{}, routes...)
}

func (u UserSpec) AllowsRelayRoute(route int) bool {
	return u.RelayRoutes == nil || slices.Contains(u.RelayRoutes, route)
}

// 线路顺序不影响权限，但缺省权限与明确的空权限必须有不同的摘要。
func (u UserSpec) RelayRoutesKey() string {
	routes := CloneRelayRoutes(u.RelayRoutes)
	slices.Sort(routes)
	routes = slices.Compact(routes)
	data, _ := json.Marshal(routes)
	return string(data)
}

func (n *NodeSpec) RelayRouteIDs() []int {
	if !n.IsRelayEntry() {
		return nil
	}
	routes := []int{n.Relay.RouteID}
	for _, child := range n.Relay.Children {
		routes = append(routes, child.RouteID)
	}
	return routes
}
