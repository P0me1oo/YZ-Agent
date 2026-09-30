package service

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestRelayEgressReportsPerDestinationRouteAndDoesNotGuessFailedRoutes(t *testing.T) {
	node := &model.NodeSpec{Relay: &model.RelayConfig{Mode: "entry", Children: []model.RelayChild{
		{NodeID: 1, Address: "first.example", Port: 28388}, {NodeID: 2, Address: "second.example", Port: 28488},
	}}}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "first.example" {
			return []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, nil
	}
	route := func(_ context.Context, address netip.Addr, _ int) (string, error) {
		if address.Is6() {
			return "", errors.New("没有 IPv6 路由")
		}
		if address.String() == "192.0.2.2" {
			return "10.0.0.1", nil
		}
		return "198.51.100.1", nil
	}
	result := collectRelayEgress(context.Background(), node, lookup, route)
	if len(result.Children) != 2 || !reflect.DeepEqual(result.Children[0].Sources, []string{"198.51.100.1"}) ||
		!reflect.DeepEqual(result.Children[1].Sources, []string{"10.0.0.1"}) {
		t.Fatalf("未按落地目标读取出口: %+v", result)
	}
	node.CustomRoutes = []map[string]any{{"outbound": "custom"}}
	result = collectRelayEgress(context.Background(), node, lookup, route)
	if len(result.Children[0].Sources) != 0 {
		t.Fatal("自定义出站不能自动认定为直出")
	}
}
