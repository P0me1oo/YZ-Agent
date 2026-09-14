package singbox

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

func TestLegacyDirectOptionsAcceptedBySingBox(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]any
	}{
		{"ipv4", M{"domainStrategy": "ForceIPv4", "send_through": "192.0.2.10"}},
		{"ipv6", M{"domainStrategy": "ForceIPv6", "send_through": "2001:db8::10"}},
		{"camel_case_binding", M{"domainStrategy": "ForceIPv4", "sendThrough": "192.0.2.10"}},
		{"equal_binding_aliases", M{"send_through": "192.0.2.10", "sendThrough": "::ffff:192.0.2.10"}},
		{"matching_native_options", M{"domainStrategy": "ForceIPv4", "domain_strategy": "ipv4_only", "send_through": "192.0.2.10", "inet4_bind_address": "192.0.2.10"}},
		{"resolver_selection", M{"domainStrategy": "ForceIPv6", "domain_resolver": M{"server": "test-dns", "strategy": "ipv6_only"}}},
		{"default_strategy", M{"domainStrategy": "AsIs"}},
		{"source_with_default_strategy", M{"domainStrategy": "AsIs", "send_through": "192.0.2.10"}},
		{"case_insensitive_strategy", M{"domainStrategy": "forceipv4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				converted, err := outboundConfigToSingbox(model.OutboundConfig{Tag: "direct", Protocol: "freedom", Settings: tc.settings})
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(M{"outbounds": []M{converted}})
				if err != nil {
					t.Fatal(err)
				}
				// 让实际固定的内核解析完整出站，避免只验证生成的 map 而遗漏未知字段。
				options, err := singJSON.UnmarshalExtendedContext[option.Options](include.Context(context.Background()), data)
				if err != nil {
					t.Fatalf("固定 sing-box 内核拒绝转换结果: %v", err)
				}
				if len(options.Outbounds) != 1 || options.Outbounds[0].Type != "direct" {
					t.Fatal("转换改变了直连出站类型")
				}
			}
			after, err := json.Marshal(tc.settings)
			if err != nil || string(after) != string(before) {
				t.Fatal("转换修改了面板原始配置，影响后续切回 Xray")
			}
		})
	}
}

func TestLegacyDirectOptionsRejectConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		settings    map[string]any
	}{
		{"strategy_type", "domainStrategy", M{"domainStrategy": 4}},
		{"unknown_strategy", "domainStrategy", M{"domainStrategy": "invalid"}},
		{"different_fallback_semantics", "domainStrategy", M{"domainStrategy": "UseIPv6v4"}},
		{"source_type", "send_through", M{"send_through": 123}},
		{"source_empty", "send_through", M{"send_through": ""}},
		{"source_hostname", "send_through", M{"send_through": "example.test"}},
		{"source_cidr", "send_through", M{"send_through": "192.0.2.0/24"}},
		{"source_unspecified", "send_through", M{"send_through": "::"}},
		{"source_mapped_unspecified", "send_through", M{"send_through": "::ffff:0.0.0.0"}},
		{"source_multicast", "send_through", M{"send_through": "ff02::1"}},
		{"source_scoped", "send_through", M{"send_through": "fe80::1%eth0"}},
		{"source_alias_conflict", "sendThrough", M{"send_through": "192.0.2.10", "sendThrough": "192.0.2.11"}},
		{"source_native_conflict", "inet4_bind_address", M{"send_through": "192.0.2.10", "inet4_bind_address": "192.0.2.11"}},
		{"dual_source_conflict", "inet6_bind_address", M{"send_through": "192.0.2.10", "inet6_bind_address": "2001:db8::10"}},
		{"force_source_conflict", "domainStrategy", M{"domainStrategy": "ForceIPv4", "send_through": "2001:db8::10"}},
		{"native_strategy_conflict", "domain_strategy", M{"domainStrategy": "ForceIPv6", "domain_strategy": "ipv4_only"}},
		{"resolver_strategy_conflict", "domain_resolver.strategy", M{"domainStrategy": "ForceIPv6", "domain_resolver": M{"server": "test-dns", "strategy": "ipv4_only"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &model.NodeSpec{Protocol: "shadowsocks", Cipher: "aes-128-gcm", CustomOutbounds: []model.OutboundConfig{{
				Tag: "direct", Protocol: "direct", Settings: tc.settings,
			}}}
			for range 2 {
				_, err := buildConfig(config.KernelConfig{}, node, nil, kernel.TLSCert{})
				if err == nil || !strings.Contains(err.Error(), "custom_outbounds[0]") || !strings.Contains(err.Error(), tc.field) {
					t.Fatalf("无效配置应指出出站位置与字段: %v", err)
				}
			}
		})
	}
}

func TestNativeDirectOptionsRemainUnchanged(t *testing.T) {
	settings := M{
		"inet4_bind_address": "192.0.2.10", "inet6_bind_address": "2001:db8::10",
		"domain_strategy": "prefer_ipv6", "fallback_delay": "300ms",
		"domain_resolver": M{"server": "test-dns", "strategy": "prefer_ipv6"},
	}
	got, err := outboundConfigToSingbox(model.OutboundConfig{Tag: "native", Protocol: "direct", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	delete(got, "tag")
	delete(got, "type")
	if !reflect.DeepEqual(got, settings) {
		t.Fatal("原生双栈配置被兼容转换改变")
	}
}
