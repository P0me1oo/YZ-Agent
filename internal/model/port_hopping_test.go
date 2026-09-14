package model

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestPortHoppingPanelRoundtripAndValidation(t *testing.T) {
	var received panel.NodeConfig
	if err := json.Unmarshal([]byte(`{"protocol":"hysteria","version":2,"server_port":8443,"port_hopping":"20000-20010,20300"}`), &received); err != nil {
		t.Fatal(err)
	}
	for _, kernel := range []string{"xray", "singbox"} {
		node, err := NodeSpecFromPanelValidated(&received, config.KernelConfig{Type: kernel})
		if err != nil {
			t.Fatal(err)
		}
		if node.ServerPort != 8443 || node.PortHopping != received.PortHopping || node.ToPanel().PortHopping != received.PortHopping {
			t.Fatal("面板配置转换丢失跳跃范围或改写实际监听端口")
		}
		for _, invalid := range []string{"20010-20000", "65536", "20000,", "0"} {
			node.PortHopping = invalid
			if ValidateNodeSpec(node, config.KernelConfig{Type: kernel}) == nil {
				t.Fatal("应拒绝无效跳跃范围")
			}
		}
		node.PortHopping, node.Protocol = received.PortHopping, "vless"
		if ValidateNodeSpec(node, config.KernelConfig{Type: kernel}) == nil {
			t.Fatal("应拒绝非 HY2 协议的跳跃配置")
		}
	}
}

func TestPortHoppingStandaloneMapping(t *testing.T) {
	cfg := &config.Config{Standalone: &config.StandaloneConfig{Node: config.StandaloneNodeConfig{Protocol: "hysteria", Version: 2, ServerPort: 8443, PortHopping: "20000-20010,20300"}}}
	node := NodeSpecFromStandalone(cfg)
	if node.PortHopping != cfg.Standalone.Node.PortHopping || node.ServerPort != 8443 {
		t.Fatal("独立模式配置转换丢失端口设置")
	}
}
