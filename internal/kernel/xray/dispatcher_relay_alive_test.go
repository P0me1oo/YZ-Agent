package xray

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
)

func relayAliveContext(source string, route xnet.Port) context.Context {
	return session.ContextWithInbound(context.Background(), &session.Inbound{
		User:       &protocol.MemoryUser{Email: userEmail(1)},
		Source:     xnet.TCPDestination(xnet.ParseAddress(source), 12345),
		VlessRoute: route,
	})
}

func TestDispatcherRelayAliveUsesRouteNode(t *testing.T) {
	ld := newTestDispatcher()
	ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	ld.innerDisp = &admissionDispatcher{}
	ld.SetRelayRoutes(relayRouteNodes(&model.NodeSpec{Relay: &model.RelayConfig{
		Mode: "entry", RouteID: 11,
		Children: []model.RelayChild{{NodeID: 7, RouteID: 12, Tag: "relay-7"}},
	}}))
	dest := xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443)

	relayLink, err := ld.Dispatch(relayAliveContext("198.51.100.7", 12), dest)
	if err != nil {
		t.Fatal(err)
	}
	directLink, err := ld.Dispatch(relayAliveContext("198.51.100.8", 11), dest)
	if err != nil {
		t.Fatal(err)
	}

	alive := ld.RelayUserAlive()
	if !alive[1][7]["198.51.100.7"] || !alive[1][0]["198.51.100.8"] || len(alive[1]) != 2 {
		t.Fatalf("在线来源没有按实际出网节点拆分: %v", alive)
	}
	_ = relayLink.Writer.(*closeTrackingWriter).Close()
	_ = relayLink.Writer.(*closeTrackingWriter).Close()
	alive = ld.RelayUserAlive()
	if len(alive[1]) != 1 || !alive[1][0]["198.51.100.8"] {
		t.Fatalf("关闭落地连接后应只剩入口直连来源: %v", alive)
	}
	_ = directLink.Writer.(*closeTrackingWriter).Close()
	if alive := ld.RelayUserAlive(); len(alive) != 0 {
		t.Fatalf("全部关闭后不应保留来源: %v", alive)
	}
}

func TestDispatcherRelayAliveDisabledOutsideEntry(t *testing.T) {
	ld := newTestDispatcher()
	ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	ld.innerDisp = &admissionDispatcher{}
	ld.SetRelayRoutes(relayRouteNodes(&model.NodeSpec{Protocol: "vless"}))
	link, err := ld.Dispatch(relayAliveContext("198.51.100.9", 12), xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443))
	if err != nil {
		t.Fatal(err)
	}
	if alive := ld.RelayUserAlive(); len(alive) != 0 {
		t.Fatalf("普通节点不应按出网节点登记来源: %v", alive)
	}
	_ = link.Writer.(*closeTrackingWriter).Close()
}

func TestDispatcherConnectionSnapshotCountsLinksFromOneSource(t *testing.T) {
	ld := newTestDispatcher()
	ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	ld.innerDisp = &admissionDispatcher{}
	ld.SetRelayRoutes(relayRouteNodes(&model.NodeSpec{Relay: &model.RelayConfig{
		Mode: "entry", RouteID: 11,
		Children: []model.RelayChild{{NodeID: 7, RouteID: 12, Tag: "relay-7"}},
	}}))
	dest := xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443)
	first, err := ld.Dispatch(relayAliveContext("198.51.100.7", 12), dest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ld.Dispatch(relayAliveContext("198.51.100.7", 12), dest)
	if err != nil {
		t.Fatal(err)
	}
	users, nodes := ld.ConnectionSnapshot()
	if users[1] != 2 || nodes[1][7] != 2 || len(ld.RelayUserAlive()[1][7]) != 1 {
		t.Fatalf("同一来源的两条连接应计为 2，而来源只计 1: users=%v nodes=%v", users, nodes)
	}
	_ = first.Writer.(*closeTrackingWriter).Close()
	_ = first.Writer.(*closeTrackingWriter).Close()
	users, nodes = ld.ConnectionSnapshot()
	if users[1] != 1 || nodes[1][7] != 1 {
		t.Fatalf("重复关闭后应只扣减一次: users=%v nodes=%v", users, nodes)
	}
	_ = second.Writer.(*closeTrackingWriter).Close()
}
