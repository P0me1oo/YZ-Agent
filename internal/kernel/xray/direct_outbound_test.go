package xray

import (
	"bytes"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/xtls/xray-core/infra/conf/serial"
)

func outboundByTag(cfg M, tag string) M {
	obs, _ := cfg["outbounds"].([]M)
	for _, ob := range obs {
		if ob["tag"] == tag {
			return ob
		}
	}
	return nil
}

// 面板允许用通用名 direct/block，xray 侧要翻译成 freedom/blackhole。
func TestCustomOutbound_DirectAndBlockAliases(t *testing.T) {
	cases := []struct {
		given string
		want  string
	}{
		{"direct", "freedom"},
		{"freedom", "freedom"},
		{"block", "blackhole"},
		{"blackhole", "blackhole"},
	}

	for _, tc := range cases {
		t.Run(tc.given, func(t *testing.T) {
			nc := &panel.NodeConfig{
				Protocol:   "shadowsocks",
				ServerPort: 1234,
				Cipher:     "aes-128-gcm",
				CustomOutbounds: []panel.OutboundConfig{{
					Tag:      "custom-out",
					Protocol: tc.given,
				}},
			}
			cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

			ob := outboundByTag(cfg, "custom-out")
			if ob == nil {
				t.Fatalf("outbound custom-out missing")
			}
			if ob["protocol"] != tc.want {
				t.Fatalf("protocol = %v, want %v", ob["protocol"], tc.want)
			}
		})
	}
}

// sendThrough 在 xray 里是 outbound 顶层字段，必须从 settings 提升上来。
func TestCustomOutbound_SendThroughIsHoisted(t *testing.T) {
	nc := &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 1234,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "direct",
			Protocol: "freedom",
			Settings: map[string]any{
				"domainStrategy": "UseIPv6v4",
				"send_through":   "2001:db8::1",
			},
		}},
	}
	cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	ob := outboundByTag(cfg, "direct")
	if ob == nil {
		t.Fatal("outbound direct missing")
	}
	if ob["sendThrough"] != "2001:db8::1" {
		t.Fatalf("sendThrough = %v, want 2001:db8::1", ob["sendThrough"])
	}

	settings, _ := ob["settings"].(map[string]any)
	if settings["domainStrategy"] != "UseIPv6v4" {
		t.Fatalf("domainStrategy = %v", settings["domainStrategy"])
	}
	if _, leaked := settings["send_through"]; leaked {
		t.Fatal("send_through must not stay inside settings")
	}
}

// 提升 sendThrough 时不能改动调用方持有的 settings map。
func TestCustomOutbound_SendThroughDoesNotMutateInput(t *testing.T) {
	settings := map[string]any{
		"domainStrategy": "UseIPv4",
		"send_through":   "203.0.113.9",
	}
	spec := &model.NodeSpec{
		Protocol:   "shadowsocks",
		ServerPort: 1234,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []model.OutboundConfig{{
			Tag:      "direct",
			Protocol: "direct",
			Settings: settings,
		}},
	}

	buildConfig(testKernelCfg, spec, testUsers, kernel.TLSCert{})

	if _, ok := settings["send_through"]; !ok {
		t.Fatal("原始 settings 被修改了")
	}
}

// 自定义的 direct 出站应当替换内置的那个，而不是共存。
func TestCustomOutbound_DirectReplacesBuiltIn(t *testing.T) {
	nc := &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 1234,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "direct",
			Protocol: "direct",
			Settings: map[string]any{"domainStrategy": "UseIPv6v4"},
		}},
	}
	cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	obs, _ := cfg["outbounds"].([]M)
	count := 0
	for _, ob := range obs {
		if ob["tag"] == "direct" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("tag direct 出现 %d 次，应当只有一个", count)
	}
	if ob := outboundByTag(cfg, "direct"); ob["settings"] == nil {
		t.Fatal("自定义 direct 没有生效，仍是内置的空 freedom")
	}
}

// 生产上 ATTV6 节点的真实需求：绑定指定的 AT&T v6 源地址，解析优先 v6。
// 生成的配置必须能通过 xray 自己的解析器。
func TestCustomOutbound_DualStackConfigParsesInXray(t *testing.T) {
	nc := &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 13432,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "direct",
			Protocol: "direct",
			Settings: map[string]any{
				"domainStrategy": "UseIPv6v4",
				"send_through":   "2600:1700:2bc1:409a:8::6675",
			},
		}},
	}

	data, err := marshalConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshalConfig: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("xray 拒绝了双栈直连配置: %v", err)
	}
}

// 拦截出站不需要任何参数。
func TestCustomOutbound_BlockNeedsNoSettings(t *testing.T) {
	nc := &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 1234,
		Cipher:     "aes-128-gcm",
		CustomOutbounds: []panel.OutboundConfig{{
			Tag:      "drop-v6",
			Protocol: "block",
		}},
	}

	data, err := marshalConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshalConfig: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("xray 拒绝了无参数的拦截出站: %v", err)
	}
}
