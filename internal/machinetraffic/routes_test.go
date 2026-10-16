package machinetraffic

import (
	"net"
	"reflect"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestDefaultRouteLinkIndices(t *testing.T) {
	destination := func(cidr string) *net.IPNet {
		t.Helper()
		_, dst, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		return dst
	}
	route := func(dst *net.IPNet, index, priority int) netlink.Route {
		return netlink.Route{Dst: dst, LinkIndex: index, Priority: priority, Type: 1, Table: 254}
	}
	otherTable := route(nil, 9, 0)
	otherTable.Table = 100
	blackhole := route(nil, 10, 0)
	blackhole.Type = 6
	multipath := route(destination("0.0.0.0/0"), 0, 10)
	multipath.MultiPath = []*netlink.NexthopInfo{{LinkIndex: 2}, {LinkIndex: 3}}
	for _, tt := range []struct {
		name   string
		routes []netlink.Route
		want   []int
	}{
		{"empty destination", []netlink.Route{route(nil, 2, 0)}, []int{2}},
		// netlink v1.3.1 将内核省略的默认目标补成零地址 /0。
		{"IPv4 normalized default", []netlink.Route{route(destination("0.0.0.0/0"), 2, 0)}, []int{2}},
		{"IPv6 normalized default", []netlink.Route{route(destination("::/0"), 3, 100)}, []int{3}},
		{"lowest metric", []netlink.Route{route(destination("0.0.0.0/0"), 2, 100), route(nil, 3, 200)}, []int{2}},
		{"equal metrics", []netlink.Route{route(destination("::/0"), 2, 100), route(destination("::/0"), 3, 100)}, []int{2, 3}},
		{"multipath", []netlink.Route{multipath}, []int{2, 3}},
		{"main unicast only", []netlink.Route{otherTable, blackhole, route(destination("0.0.0.0/0"), 2, 100)}, []int{2}},
		{"specific routes only", []netlink.Route{route(destination("0.0.0.0/1"), 2, 0), route(destination("::/1"), 3, 0), route(destination("192.0.2.0/24"), 4, 0)}, nil},
		{"invalid destinations", []netlink.Route{route(&net.IPNet{}, 2, 0), route(&net.IPNet{IP: net.IPv4zero, Mask: net.IPMask{0, 255, 0, 0}}, 3, 0)}, nil},
		{"specific route metric ignored", []netlink.Route{route(destination("192.0.2.0/24"), 4, 0), route(destination("0.0.0.0/0"), 2, 100)}, []int{2}},
		{"no valid link", []netlink.Route{route(nil, 0, 0)}, nil},
		{"no routes", nil, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultRouteLinkIndices(tt.routes)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("default route links = %v, want %v", got, tt.want)
			}
		})
	}
}
