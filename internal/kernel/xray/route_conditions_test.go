package xray

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// 没有附加条件的面板路由必须与旧版生成完全相同的规则。
func TestCompilePanelRouteRule_LegacyOutputUnchanged(t *testing.T) {
	got := compilePanelRouteRule(testRouteRules([]panel.RouteRule{{
		ID: 1, Match: []string{"*.example.com", "geosite:cn", " ", "10.0.0.0/8", "geoip:private"}, Action: "proxy", ActionValue: "warp",
	}})[0])
	want := []M{
		{"type": "field", "domain": []string{"geosite:cn", "domain:example.com"}, "outboundTag": "warp"},
		{"type": "field", "ip": []string{"10.0.0.0/8", "geoip:private"}, "outboundTag": "warp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rules = %#v, want %#v", got, want)
	}
}

// 附加条件写入每条地址规则，只有附加条件时生成一条不限地址的规则。
func TestCompilePanelRouteRule_Conditions(t *testing.T) {
	routes := testRouteRules([]panel.RouteRule{
		{ID: 1, Match: []string{"example.com", "1.1.1.0/24"}, Action: "block", Protocol: []string{"bittorrent"}, Port: "25,6881-6889", Network: "udp"},
		{ID: 2, Action: "block", Protocol: []string{"bittorrent"}},
		{ID: 3, Action: "direct", Port: "443", Network: "udp"},
	})
	got := compilePanelRouteRule(routes[0])
	want := []M{
		{"type": "field", "domain": []string{"domain:example.com"}, "protocol": []string{"bittorrent"}, "port": "25,6881-6889", "network": "udp", "outboundTag": "block"},
		{"type": "field", "ip": []string{"1.1.1.0/24"}, "protocol": []string{"bittorrent"}, "port": "25,6881-6889", "network": "udp", "outboundTag": "block"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("地址加条件 = %#v, want %#v", got, want)
	}
	if got := compilePanelRouteRule(routes[1]); !reflect.DeepEqual(got, []M{{"type": "field", "protocol": []string{"bittorrent"}, "outboundTag": "block"}}) {
		t.Fatalf("只有协议 = %#v", got)
	}
	if got := compilePanelRouteRule(routes[2]); !reflect.DeepEqual(got, []M{{"type": "field", "port": "443", "network": "udp", "outboundTag": "direct"}}) {
		t.Fatalf("端口加网络 = %#v", got)
	}
}

// 无法识别的条件、无效地址和空路由都不能生成规则，避免扩大匹配范围。
func TestCompilePanelRouteRule_SkipsUnsafeRoutes(t *testing.T) {
	for name, route := range map[string]panel.RouteRule{
		"未知协议":    {ID: 1, Match: []string{"example.com"}, Action: "block", Protocol: []string{"quic"}},
		"无效端口":    {ID: 2, Action: "block", Port: "70000"},
		"未知网络":    {ID: 3, Action: "block", Network: "icmp"},
		"地址全部无效":  {ID: 4, Match: []string{"*."}, Action: "block", Protocol: []string{"bittorrent"}},
		"没有任何条件":  {ID: 5, Action: "block"},
		"只有空白匹配值": {ID: 6, Match: []string{" ", ""}, Action: "block"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := compilePanelRouteRule(testRouteRules([]panel.RouteRule{route})[0]); got != nil {
				t.Fatalf("rules = %#v, want nil", got)
			}
		})
	}
}

func btRouteNode(nc *panel.NodeConfig) *panel.NodeConfig {
	nc.Routes = append(nc.Routes, panel.RouteRule{ID: 21, Action: "block", Protocol: []string{"bittorrent"}})
	return nc
}

// 只有绑定了协议条件的节点开启嗅探，并且不改写目标地址。
func TestBuildConfig_SniffingFollowsProtocolRoutes(t *testing.T) {
	plain := &panel.NodeConfig{Protocol: "vless", ServerPort: 24443, Network: "tcp", Decryption: "none",
		Routes: []panel.RouteRule{{ID: 1, Match: []string{"example.com"}, Action: "block", Port: "25"}}}
	inbound := buildConfig(testKernelCfg, testNodeSpec(plain), testUsers, kernel.TLSCert{})["inbounds"].([]M)[0]
	if _, ok := inbound["sniffing"]; ok {
		t.Fatalf("没有协议条件时不应开启嗅探: %#v", inbound["sniffing"])
	}

	for name, nc := range map[string]*panel.NodeConfig{
		"普通节点": btRouteNode(&panel.NodeConfig{Protocol: "vless", ServerPort: 24443, Network: "tcp", Decryption: "none"}),
		"落地节点": btRouteNode(relayLandingNode()),
		"中转入口": btRouteNode(relayEntryNode()),
	} {
		t.Run(name, func(t *testing.T) {
			spec := testNodeSpec(nc)
			inbounds := buildConfig(testKernelCfg, spec, testUsers, kernel.TLSCert{})["inbounds"].([]M)
			if got := inbounds[0]["sniffing"]; !reflect.DeepEqual(got, M{"enabled": true}) {
				t.Fatalf("sniffing = %#v, want enabled without destOverride", got)
			}
			raw, err := marshalConfig(testKernelCfg, spec, testUsers, kernel.TLSCert{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := serial.LoadJSONConfig(bytes.NewReader(raw)); err != nil {
				t.Fatalf("xray rejected config: %v", err)
			}
		})
	}
}

// 中转入口：落地编号规则排在面板路由前，入口自身编号的直连规则排在面板路由之后。
func TestBuildConfig_RelayEntryPanelRoutesApplyToEntryUsers(t *testing.T) {
	cfg := buildConfig(testKernelCfg, testNodeSpec(btRouteNode(relayEntryNode())), testUsers, kernel.TLSCert{})
	rules := cfg["routing"].(M)["rules"].([]M)
	position := map[string]int{}
	for i, rule := range rules {
		if route, ok := rule["vlessRoute"].(string); ok {
			position["route-"+route] = i
		}
		if _, ok := rule["protocol"]; ok {
			position["bt"] = i
		}
	}
	for _, key := range []string{"route-11", "route-12", "route-13", "bt"} {
		if _, ok := position[key]; !ok {
			t.Fatalf("missing %s in %#v", key, rules)
		}
	}
	if position["route-12"] > position["bt"] || position["route-13"] > position["bt"] {
		t.Fatalf("落地编号规则必须排在面板路由之前: %v", position)
	}
	if position["route-11"] < position["bt"] {
		t.Fatalf("入口自身的直连规则必须排在面板路由之后: %v", position)
	}
	if position["route-11"] != len(rules)-1 {
		t.Fatalf("入口直连规则应为最后一条: %v of %d", position, len(rules))
	}
}
