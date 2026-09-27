package singbox

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

// 没有附加条件的面板路由必须与旧版生成完全相同的规则。
func TestCompilePanelRouteRule_LegacyOutputUnchanged(t *testing.T) {
	got := compilePanelRouteRule(testRouteRules([]panel.RouteRule{{
		ID: 1, Match: []string{"*.example.com", " ", "example.org", "10.0.0.0/8"}, Action: "proxy", ActionValue: "warp",
	}})[0])
	want := []M{
		{"domain_suffix": []string{"example.com", "example.org"}, "outbound": "warp"},
		{"ip_cidr": []string{"10.0.0.0/8"}, "outbound": "warp"},
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
		{ID: 3, Action: "direct", Port: "443", Network: "tcp"},
	})
	got := compilePanelRouteRule(routes[0])
	want := []M{
		{"domain_suffix": []string{"example.com"}, "protocol": []string{"bittorrent"}, "port": []int{25}, "port_range": []string{"6881:6889"}, "network": []string{"udp"}, "outbound": "block"},
		{"ip_cidr": []string{"1.1.1.0/24"}, "protocol": []string{"bittorrent"}, "port": []int{25}, "port_range": []string{"6881:6889"}, "network": []string{"udp"}, "outbound": "block"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("地址加条件 = %#v, want %#v", got, want)
	}
	if got := compilePanelRouteRule(routes[1]); !reflect.DeepEqual(got, []M{{"protocol": []string{"bittorrent"}, "outbound": "block"}}) {
		t.Fatalf("只有协议 = %#v", got)
	}
	if got := compilePanelRouteRule(routes[2]); !reflect.DeepEqual(got, []M{{"port": []int{443}, "network": []string{"tcp"}, "outbound": "direct"}}) {
		t.Fatalf("端口加网络 = %#v", got)
	}
}

// 无法识别的条件、无效地址和空路由都不能生成规则，避免扩大匹配范围。
func TestCompilePanelRouteRule_SkipsUnsafeRoutes(t *testing.T) {
	for name, route := range map[string]panel.RouteRule{
		"未知协议":    {ID: 1, Match: []string{"example.com"}, Action: "block", Protocol: []string{"quic"}},
		"无效端口":    {ID: 2, Action: "block", Port: "0"},
		"未知网络":    {ID: 3, Action: "block", Network: "icmp"},
		"地址全部无效":  {ID: 4, Match: []string{"*."}, Action: "block", Network: "udp"},
		"没有任何条件":  {ID: 5, Action: "block"},
		"只有空白匹配值": {ID: 6, Match: []string{" "}, Action: "block"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := compilePanelRouteRule(testRouteRules([]panel.RouteRule{route})[0]); got != nil {
				t.Fatalf("rules = %#v, want nil", got)
			}
		})
	}
}

// 嗅探只出现一次，紧挨在第一条按协议匹配的面板路由之前；没有协议条件时不嗅探。
func TestBuildRoutes_SniffBeforeFirstProtocolRule(t *testing.T) {
	plain := buildRoutes(testRouteRules([]panel.RouteRule{{ID: 1, Action: "block", Port: "25"}}), nil, nil)["rules"].([]M)
	for _, rule := range plain {
		if rule["action"] == "sniff" {
			t.Fatalf("没有协议条件时不应嗅探: %#v", plain)
		}
	}

	rules := buildRoutes(testRouteRules([]panel.RouteRule{
		{ID: 1, Match: []string{"example.com"}, Action: "direct"},
		{ID: 2, Action: "block", Protocol: []string{"bittorrent"}},
		{ID: 3, Match: []string{"example.org"}, Action: "block", Protocol: []string{"bittorrent"}, Network: "udp"},
	}), nil, nil)["rules"].([]M)
	sniffAt, firstProtocolAt, domainAt := -1, -1, -1
	for i, rule := range rules {
		switch {
		case rule["action"] == "sniff":
			if sniffAt >= 0 {
				t.Fatalf("嗅探重复出现: %#v", rules)
			}
			sniffAt = i
			if !reflect.DeepEqual(rule["sniffer"], []string{"bittorrent"}) {
				t.Fatalf("sniffer = %#v", rule["sniffer"])
			}
		case rule["protocol"] != nil && firstProtocolAt < 0:
			firstProtocolAt = i
		case reflect.DeepEqual(rule["domain_suffix"], []string{"example.com"}):
			domainAt = i
		}
	}
	if sniffAt < 0 || sniffAt+1 != firstProtocolAt || domainAt > sniffAt {
		t.Fatalf("sniff=%d protocol=%d domain=%d: %#v", sniffAt, firstProtocolAt, domainAt, rules)
	}
}

// 中转入口：落地线路在解析前选路，入口自身线路的直连排在面板路由之后。
func TestBuildRoutes_RelayEntryPanelRoutesApplyToEntryUsers(t *testing.T) {
	node := testRelayNode()
	node.Routes = testRouteRules([]panel.RouteRule{{ID: 1, Action: "block", Protocol: []string{"bittorrent"}}})
	rules := buildRoutes(node.Routes, nil, nil, buildRelayRoutingRules(node, []model.UserSpec{runtimeUser(t, 1100)})...)["rules"].([]M)
	relayAt, resolveAt, btAt, directAt := -1, -1, -1, -1
	for i, rule := range rules {
		switch {
		case rule["outbound"] == "relay-7":
			relayAt = i
		case rule["action"] == "resolve":
			resolveAt = i
		case rule["protocol"] != nil:
			btAt = i
		case rule["outbound"] == "direct" && rule["auth_user"] != nil:
			directAt = i
		}
	}
	if !(relayAt >= 0 && relayAt < resolveAt && resolveAt < btAt && btAt < directAt && directAt == len(rules)-1) {
		t.Fatalf("relay=%d resolve=%d bt=%d direct=%d: %#v", relayAt, resolveAt, btAt, directAt, rules)
	}
}

// 生成的配置必须被 sing-box 自身接受，包括嗅探动作和各项附加条件。
func TestBuildConfig_ProtocolRoutesAcceptedBySingBox(t *testing.T) {
	for name, nc := range map[string]*panel.NodeConfig{
		"普通节点": {Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: 24443, Network: "tcp"},
		"中转入口": {Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: 24443, Network: "tcp", Relay: &panel.RelayConfig{
			Mode: panel.RelayModeEntry, RouteID: 11, Children: []panel.RelayChild{{
				NodeID: 7, RouteID: 12, Tag: "relay-7", Protocol: "shadowsocks", Address: "127.0.0.1", Port: 28388,
				Cipher: "aes-128-gcm", Password: "relay-test-password",
			}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			nc.Routes = []panel.RouteRule{
				{ID: 1, Match: []string{"example.com", "1.1.1.0/24"}, Action: "block", Protocol: []string{"bittorrent"}, Port: "25,6881-6889", Network: "udp"},
				{ID: 2, Action: "block", Protocol: []string{"bittorrent"}},
				{ID: 3, Action: "direct", Port: "443", Network: "tcp"},
			}
			cfg, err := buildConfig(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()},
				testNodeSpec(nc), testUsers, kernel.TLSCert{})
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx := include.Context(context.Background())
			options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
			if err != nil {
				t.Fatalf("sing-box 拒绝解析配置: %v", err)
			}
			instance, err := box.New(box.Options{Context: ctx, Options: options})
			if err != nil {
				t.Fatalf("sing-box 拒绝创建实例: %v", err)
			}
			_ = instance.Close()
		})
	}
}
