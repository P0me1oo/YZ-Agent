package model

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"gopkg.in/yaml.v3"
)

func TestProxyProtocolConfigCompatibility(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want *bool
	}{
		{`{}`, new(false)},
		{`{"accept_proxy_protocol":true}`, new(true)},
		{`{"accept_proxy_protocol":false,"networkSettings":{"acceptProxyProtocol":true}}`, new(false)},
		{`{"networkSettings":{"acceptProxyProtocol":true}}`, new(true)},
		{`{"networkSettings":{"acceptProxyProtocol":false}}`, new(false)},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			var cfg panel.NodeConfig
			if err := json.Unmarshal([]byte(tc.raw), &cfg); err != nil {
				t.Fatal(err)
			}
			spec := NodeSpecFromPanel(&cfg)
			got := spec.ProxyProtocolSetting()
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatal("接收开关优先级或旧配置状态丢失")
			}
			if cfg.GetProxyProtocol() != spec.GetProxyProtocol() {
				t.Fatal("面板与模型开关不一致")
			}
			roundtrip := NodeSpecFromPanel(spec.ToPanel())
			if roundtrip.GetProxyProtocol() != spec.GetProxyProtocol() {
				t.Fatal("回写开关丢失")
			}
		})
	}
	for _, value := range []bool{true, false} {
		var cfg config.StandaloneNodeConfig
		raw := "accept_proxy_protocol: false\n"
		if value {
			raw = "accept_proxy_protocol: true\n"
		}
		if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.AcceptProxyProtocol == nil || *cfg.AcceptProxyProtocol != value {
			t.Fatal("独立配置的开关状态丢失")
		}
	}
}
