package xray

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	xrayCore "github.com/xtls/xray-core/core"
)

func TestRelayPermissionUpdateKeepsInstanceAndRevokesRetiredConnections(t *testing.T) {
	node := &model.NodeSpec{Relay: &model.RelayConfig{Mode: "entry", RouteID: 11,
		Children: []model.RelayChild{{RouteID: 12}}}}
	users := []model.UserSpec{{ID: 1, RelayRoutes: []int{11, 12}}}
	current, previous := newTestDispatcher(), newTestDispatcher()
	for _, dispatcher := range []*LimitDispatcher{current, previous} {
		dispatcher.SetRelayRoutes(relayRouteNodes(node))
		dispatcher.SetRelayAccess(node, users)
	}
	closed := 0
	if !previous.relayAccess.Register("old", userEmail(1)+":12", func() { closed++ }) {
		t.Fatal("旧实例无法登记有效连接")
	}
	x := New(config.KernelConfig{Type: "xray"})
	x.instance, x.nodeConfig, x.users = new(xrayCore.Instance), node, users
	x.limitDispatcher = current
	x.retired = []*retiredXray{{dispatcher: previous}}
	instance := x.instance
	users = []model.UserSpec{{ID: 1, RelayRoutes: []int{11}}}
	if _, _, err := x.UpdateUsers(users); err != nil {
		t.Fatal(err)
	}
	if closed != 1 || current.relayAccess.Allows(userEmail(1)+":12") || previous.relayAccess.Allows(userEmail(1)+":12") {
		t.Fatal("新旧实例未同时撤权并关闭旧连接")
	}
	// 相同快照必须只刷新限制；误重建空测试实例会失败。
	if err := x.Reload(node, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	if x.instance != instance || closed != 1 || !previous.relayAccess.Allows(userEmail(1)+":11") {
		t.Fatal("重复快照重建实例、重复关闭连接或影响保留线路")
	}
	x.nodeConfig = &model.NodeSpec{}
	x.updateDispatcherLimits(users)
	if previous.relayAccess.Allows(userEmail(1) + ":11") {
		t.Fatal("移除中转拓扑后旧实例仍放行旧线路")
	}
}
