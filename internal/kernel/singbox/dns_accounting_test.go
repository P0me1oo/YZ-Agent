package singbox

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

func TestDNSAccountingPreservesRulesAndCustomTag(t *testing.T) {
	rules := []M{
		{"port": 80, "outbound": "block"},
		{"port": 53, "action": "hijack-dns"},
		{"type": "logical", "mode": "or", "rules": []M{{"port": 5353}}, "action": "hijack-dns"},
	}
	original, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 1234, CustomRoutes: rules}
	local := config.KernelConfig{CustomOutbound: []M{{"type": "direct", "tag": "yz-dns"}}}
	cfg, err := buildConfig(local, node, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(rules)
	if string(after) != string(original) {
		t.Fatal("DNS 适配改写了面板持有的原规则")
	}
	compiled := cfg["route"].(M)["rules"].([]M)
	dnsTargetsMatch := compiled[1]["outbound"] == "yz-dns-1" && compiled[2]["outbound"] == "yz-dns-1"
	if !reflect.DeepEqual(compiled[0], rules[0]) || !dnsTargetsMatch {
		t.Fatalf("规则顺序、条件或自定义标签发生改变: %v", compiled[:3])
	}
	if compiled[1]["action"] != "route" || !reflect.DeepEqual(compiled[2]["rules"], rules[2]["rules"]) {
		t.Fatal("DNS 逻辑规则条件发生改变")
	}
	before, _ := json.Marshal(cfg)
	prepareDNSAccounting(cfg)
	after, _ = json.Marshal(cfg)
	if string(after) != string(before) {
		t.Fatal("重复适配新增出站或改变规则")
	}
	ctx, err := directOutboundContext(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, before); err != nil {
		t.Fatalf("DNS 适配后的配置不能由锁定核心解析: %v", err)
	}
}

func TestDNSAccountingLeavesNonDNSConfigurationUnchanged(t *testing.T) {
	cfg := M{"route": M{"rules": []M{{"outbound": "direct"}}}, "outbounds": []M{{"type": "direct", "tag": "direct"}}}
	before, _ := json.Marshal(cfg)
	prepareDNSAccounting(cfg)
	after, _ := json.Marshal(cfg)
	if string(before) != string(after) {
		t.Fatal("未启用 DNS 接管时增加了出站或修改了规则")
	}
}
