package model

import (
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestSourcePolicyPanelRoundTripAndWireGuardExclusion(t *testing.T) {
	var input panel.NodeConfig
	if err := json.Unmarshal([]byte(`{"protocol":"vless","source_policy":{"mode":"blacklist","block_cn":true,"allow_ips":["198.51.100.7"],"block_ips":["203.0.113.7"]}}`), &input); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpecFromPanel(&input)
	if err := ValidateNodeSpec(spec, config.KernelConfig{Type: "xray"}); err != nil {
		t.Fatal(err)
	}
	output := spec.ToPanel()
	if output.SourcePolicy.Mode != "blacklist" || output.SourcePolicy.BlockIPs[0] != "203.0.113.7" {
		t.Fatal("名单模式或黑名单转换丢失")
	}
	output.SourcePolicy.BlockIPs[0] = "203.0.113.8"
	if spec.SourcePolicy.BlockIPs[0] != "203.0.113.7" || input.SourcePolicy.BlockIPs[0] != "203.0.113.7" {
		t.Fatal("配置转换共享了可变黑名单")
	}
	output.SourcePolicy.AllowIPs[0] = "198.51.100.8"
	if spec.SourcePolicy.AllowIPs[0] != "198.51.100.7" || input.SourcePolicy.AllowIPs[0] != "198.51.100.7" {
		t.Fatal("配置转换共享了可变例外列表")
	}
	spec.Protocol = "wireguard"
	if err := ValidateNodeSpec(spec, config.KernelConfig{Type: "xray"}); err == nil {
		t.Fatal("缺少服务端身份的普通 WireGuard 节点不应启动")
	}
}
