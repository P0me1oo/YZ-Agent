package singbox

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	srule "github.com/sagernet/sing-box/route/rule"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
	xgeo "github.com/xtls/xray-core/common/geodata"
	"github.com/xtls/xray-core/common/platform"
	"google.golang.org/protobuf/proto"
)

func geoFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]proto.Message{
		"geosite.dat": &xgeo.GeoSiteList{Entry: []*xgeo.GeoSite{{Code: "TEST", Domain: []*xgeo.Domain{
			{Type: xgeo.Domain_Full, Value: "FULL.test"},
			{Type: xgeo.Domain_Domain, Value: "suffix.test"},
			{Type: xgeo.Domain_Substr, Value: "keyword"},
			{Type: xgeo.Domain_Regex, Value: `^r[0-9]+\.test$`},
		}}}},
		"geoip.dat": &xgeo.GeoIPList{Entry: []*xgeo.GeoIP{{Code: "TEST", Cidr: []*xgeo.CIDR{{Ip: []byte{192, 0, 2, 0}, Prefix: 24}}}}},
	}
	for name, message := range files {
		data, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPrepareGeoRuleSetsPreservesCustomLogicalSets(t *testing.T) {
	t.Parallel()
	custom := M{"type": "inline", "tag": "custom", "rules": []M{{"type": "logical", "mode": "or", "rules": []M{{"domain": []string{"keep.test"}}}}}}
	cfg := M{"route": M{"rule_set": []any{custom}, "rules": []any{
		M{"type": "logical", "mode": "or", "rules": []M{{"rule_set": []string{"custom", "geosite:test"}}}},
		M{"rule_set": []string{"geosite:test"}, "port": []int{443}, "outbound": "warp"},
	}}}
	dir := geoFixture(t)
	if err := prepareGeoRuleSets(cfg, dir); err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(M)
	sets := route["rule_set"].([]M)
	if len(sets) != 2 || !reflect.DeepEqual(sets[0], custom) {
		t.Fatalf("custom logical set changed or duplicated: %#v", sets)
	}
	rule := route["rules"].([]any)[1].(M)
	if rule["outbound"] != "warp" || !reflect.DeepEqual(rule["port"], []int{443}) ||
		!reflect.DeepEqual(rule["rule_set"], []string{sets[1]["tag"].(string)}) {
		t.Fatalf("route conditions changed: %#v", rule)
	}
	first, _ := json.Marshal(cfg)
	if err := prepareGeoRuleSets(cfg, dir); err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(cfg)
	if string(first) != string(second) {
		t.Fatal("repeated preparation changed config")
	}
	conflict := M{"route": M{"rule_set": []M{sets[1]}, "rules": []M{{"rule_set": "geosite:test"}}}}
	if err := prepareGeoRuleSets(conflict, dir); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting tag accepted: %v", err)
	}
}

func TestGeoRuleSetsMatchDomainTypes(t *testing.T) {
	dir := geoFixture(t)
	t.Setenv(platform.AssetLocation, dir)
	xrules, err := xgeo.ParseDomainRules([]string{"geosite:test"}, xgeo.Domain_Substr)
	if err != nil {
		t.Fatal(err)
	}
	xmatcher, err := xgeo.DomainReg.BuildDomainMatcher(xrules)
	if err != nil {
		t.Fatal(err)
	}
	cfg := M{"route": M{"rules": []M{{"rule_set": []string{"geosite:test"}}}}}
	if err := prepareGeoRuleSets(cfg, dir); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cfg["route"].(M)["rule_set"].([]M)[0]["rules"].([]M)[0])
	var options option.HeadlessRule
	if err := json.Unmarshal(data, &options); err != nil {
		t.Fatal(err)
	}
	rule, err := srule.NewHeadlessRule(include.Context(context.Background()), options)
	if err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]bool{
		"full.test": true, "child.full.test": false, "suffix.test": true, "child.suffix.test": true,
		"notsuffix.test": false, "a-keyword.test": true, "r123.test": true, "rno.test": false,
		"unrelated.test": false,
	} {
		t.Run(domain, func(t *testing.T) {
			if got := rule.Match(&adapter.InboundContext{Domain: domain}); got != want || xmatcher.MatchAny(domain) != want {
				t.Fatalf("match = %v, want %v", got, want)
			}
		})
	}
}

func TestBuildConfigGeoRulesAcceptedBySingBox(t *testing.T) {
	t.Parallel()
	node := &model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: 24443, Network: "tcp",
		Routes: []model.RouteRule{
			{ID: 1, Action: "block", Match: []string{"geosite:test", "geoip:test"}, Ports: []string{"443"}},
			{ID: 2, Action: "proxy", ActionValue: "warp", Match: []string{"geosite:test"}},
		},
	}
	kcfg := config.KernelConfig{Type: "singbox", LogLevel: "fatal", GeoDataDir: geoFixture(t), ConfigDir: t.TempDir(),
		CustomOutbound: []map[string]any{{"tag": "warp", "type": "direct"}},
	}
	cfg, err := buildConfig(kcfg, node, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cfg)
	ctx := include.Context(context.Background())
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	_ = instance.Close()
	node.Routes[0].Match = []string{"geosite:missing"}
	if _, err := buildConfig(kcfg, node, testUsers, kernel.TLSCert{}); err == nil {
		t.Fatal("missing category accepted")
	}
	// 清除面板路由后不再加载分类文件，支持停用全部规则后的恢复。
	node.Routes = nil
	kcfg.GeoDataDir = filepath.Join(t.TempDir(), "unused")
	cfg, err = buildConfig(kcfg, node, testUsers, kernel.TLSCert{})
	if err != nil || cfg["route"].(M)["rule_set"] != nil {
		t.Fatalf("cleared routes = %#v, %v", cfg, err)
	}
	if _, err := os.Stat(kcfg.GeoDataDir); !os.IsNotExist(err) {
		t.Fatalf("unused data directory created: %v", err)
	}
}

func TestGeoRuleSetsMatchIPLikeXray(t *testing.T) {
	dir := geoFixture(t)
	t.Setenv(platform.AssetLocation, dir)
	for _, ref := range []string{"geoip:test", "geoip:!test"} {
		t.Run(ref, func(t *testing.T) {
			xrules, err := xgeo.ParseIPRules([]string{ref})
			if err != nil {
				t.Fatal(err)
			}
			xmatcher, err := xgeo.IPReg.BuildIPMatcher(xrules)
			if err != nil {
				t.Fatal(err)
			}
			cfg := M{"route": M{"rules": []M{{"rule_set": []string{ref}}}}}
			if err := prepareGeoRuleSets(cfg, dir); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(cfg["route"].(M)["rule_set"].([]M)[0]["rules"].([]M)[0])
			var options option.HeadlessRule
			if err := json.Unmarshal(data, &options); err != nil {
				t.Fatal(err)
			}
			rule, err := srule.NewHeadlessRule(include.Context(context.Background()), options)
			if err != nil {
				t.Fatal(err)
			}
			for _, ip := range []string{"192.0.2.10", "198.51.100.1", "2001:db8::1"} {
				want := xmatcher.Match(net.ParseIP(ip))
				for _, metadata := range []adapter.InboundContext{
					{Destination: singM.ParseSocksaddrHostPort(ip, 443)},
					{Domain: "resolve.test", DestinationAddresses: []netip.Addr{netip.MustParseAddr(ip)}},
				} {
					if got := rule.Match(&metadata); got != want {
						t.Fatalf("%s %s: sing-box=%v xray=%v", ref, ip, got, want)
					}
				}
			}
		})
	}
}

func TestGeoRulesReloadFailureAndRecovery(t *testing.T) {
	node := runtimeNode(t, "vless")
	node.CustomRoutes = nil
	node.Routes = []model.RouteRule{{ID: 1, Action: "block", Match: []string{"geosite:test"}}}
	user := runtimeUser(t, 1401)
	core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", GeoDataDir: geoFixture(t), ConfigDir: t.TempDir()})
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	before := core.box
	invalid := *node
	invalid.Routes = []model.RouteRule{{ID: 1, Action: "block", Match: []string{"geosite:missing"}}}
	if err := core.Reload(&invalid, []model.UserSpec{user}, kernel.TLSCert{}); err == nil {
		t.Fatal("missing category accepted on reload")
	}
	if core.box != before || core.nodeConfig != node {
		t.Fatal("failed category replaced active state")
	}
	for range 2 {
		if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
	}
	cleared := *node
	cleared.Routes = nil
	if err := core.Reload(&cleared, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	if len(core.nodeConfig.Routes) != 0 {
		t.Fatal("classification retained after clearing routes")
	}
}
