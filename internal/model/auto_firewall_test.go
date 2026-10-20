package model

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestAutoFirewallDefaultsAndPanelRoundTrip(t *testing.T) {
	if !(*NodeSpec)(nil).ManagesFirewall() || !(&NodeSpec{}).ManagesFirewall() {
		t.Fatal("旧配置缺少开关时必须默认开启")
	}
	for _, enabled := range []bool{false, true} {
		var input panel.NodeConfig
		if err := json.Unmarshal([]byte(`{"protocol":"vless","server_port":43222,"auto_firewall":`+map[bool]string{false: "false", true: "true"}[enabled]+`}`), &input); err != nil {
			t.Fatal(err)
		}
		node := NodeSpecFromPanel(&input)
		if node.ManagesFirewall() != enabled {
			t.Fatal("开关在面板配置转换时丢失")
		}
		*input.AutoFirewall = !enabled
		if node.ManagesFirewall() != enabled {
			t.Fatal("运行配置与面板输入共用了开关指针")
		}
		output := node.ToPanel()
		if output.AutoFirewall == nil || *output.AutoFirewall != enabled {
			t.Fatal("开关未写回面板配置")
		}
		*output.AutoFirewall = !enabled
		if node.ManagesFirewall() != enabled {
			t.Fatal("写回配置共用了开关指针")
		}
	}
}
