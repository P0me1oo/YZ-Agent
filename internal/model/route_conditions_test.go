package model

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestPanelRouteConditionsNormalizes(t *testing.T) {
	got, err := PanelRouteConditions(RouteRule{
		Protocols: []string{" BitTorrent ", "bittorrent", ""},
		Ports:     []string{"25", " 6881-6889 ", "6881:6889", "443-443", ""},
		Networks:  []string{"UDP", "udp", " "},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := RouteConditions{
		Protocols: []string{"bittorrent"},
		Ports:     []string{"25", "6881-6889", "443"},
		Networks:  []string{"udp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conditions = %#v, want %#v", got, want)
	}
	if got.Empty() {
		t.Fatal("有条件的路由不应视为空")
	}
	if empty, err := PanelRouteConditions(RouteRule{Match: []string{"example.com"}}); err != nil || !empty.Empty() {
		t.Fatalf("只有目标地址时附加条件应为空: %#v, %v", empty, err)
	}
}

// 无法识别的条件必须让整条路由失效，而不是丢掉条件后扩大匹配范围。
func TestPanelRouteConditionsRejectsUnknownValues(t *testing.T) {
	for name, route := range map[string]RouteRule{
		"未知协议":   {Protocols: []string{"quic"}},
		"端口为零":   {Ports: []string{"0"}},
		"端口超界":   {Ports: []string{"65536"}},
		"范围颠倒":   {Ports: []string{"9000-8000"}},
		"范围格式错误": {Ports: []string{"1-2-3"}},
		"非数字端口":  {Ports: []string{"http"}},
		"未知网络":   {Networks: []string{"icmp"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PanelRouteConditions(route); err == nil {
				t.Fatalf("%#v 应被拒绝", route)
			}
		})
	}
}

func TestRouteSniffProtocols(t *testing.T) {
	routes := []RouteRule{
		{Match: []string{"example.com"}},
		{Protocols: []string{"quic"}},
		{Protocols: []string{"BitTorrent"}, Networks: []string{"udp"}},
		{Protocols: []string{"bittorrent"}},
	}
	if got := RouteSniffProtocols(routes); !reflect.DeepEqual(got, []string{"bittorrent"}) {
		t.Fatalf("sniff protocols = %#v", got)
	}
	if got := RouteSniffProtocols(routes[:2]); len(got) != 0 {
		t.Fatalf("没有有效协议条件时不应开启嗅探: %#v", got)
	}
}

func TestPanelRouteFieldsRoundTrip(t *testing.T) {
	var nc panel.NodeConfig
	payload := `{"protocol":"vless","routes":[
		{"id":3,"match":[],"action":"block","protocol":["bittorrent"],"port":"25, 6881-6889","network":"udp"},
		{"id":4,"match":["example.com"],"action":"direct"}
	]}`
	if err := json.Unmarshal([]byte(payload), &nc); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpecFromPanel(&nc)
	want := RouteRule{ID: 3, Action: "block", Protocols: []string{"bittorrent"}, Ports: []string{"25", "6881-6889"}, Networks: []string{"udp"}}
	if !reflect.DeepEqual(spec.Routes[0], want) {
		t.Fatalf("route = %#v, want %#v", spec.Routes[0], want)
	}
	if plain := spec.Routes[1]; plain.Protocols != nil || plain.Ports != nil || plain.Networks != nil {
		t.Fatalf("未设置附加条件的路由应保持为空: %#v", plain)
	}
	back := spec.ToPanel().Routes[0]
	if back.Port != "25,6881-6889" || back.Network != "udp" || !reflect.DeepEqual(back.Protocol, []string{"bittorrent"}) {
		t.Fatalf("ToPanel route = %#v", back)
	}
}

// 已有路由没有附加条件时，序列化结果必须与旧版相同，升级后不因配置哈希变化而重载内核。
func TestRouteRuleSerializationKeepsLegacyShape(t *testing.T) {
	data, err := json.Marshal(RouteRule{ID: 1, Match: []string{"example.com"}, Action: "block"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"ID":1,"Match":["example.com"],"Action":"block","ActionValue":""}` {
		t.Fatalf("legacy route json = %s", data)
	}
}

func TestStandaloneRouteConditions(t *testing.T) {
	spec := NodeSpecFromStandalone(&config.Config{Standalone: &config.StandaloneConfig{
		Enabled: true,
		Node: config.StandaloneNodeConfig{Protocol: "vless", Routes: []config.StandaloneRouteRule{{
			ID: 1, Action: "block", Protocol: []string{"bittorrent"}, Port: "6881-6889", Network: "tcp",
		}}},
	}})
	want := RouteRule{ID: 1, Action: "block", Protocols: []string{"bittorrent"}, Ports: []string{"6881-6889"}, Networks: []string{"tcp"}}
	if !reflect.DeepEqual(spec.Routes[0], want) {
		t.Fatalf("standalone route = %#v, want %#v", spec.Routes[0], want)
	}
}
