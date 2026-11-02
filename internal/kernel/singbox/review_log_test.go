package singbox

import (
	"bytes"
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	blog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"strings"
	"testing"
)

func TestPanelLogLevelAndClearedOverrideReachSingBoxConfiguration(t *testing.T) {
	local := config.KernelConfig{LogLevel: "warn"}
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
	for _, item := range []struct{ remote, expected string }{{"debug", "debug"}, {"info", "info"}, {"", "warn"}, {"none", "disabled"}} {
		node.KernelLogLevel = item.remote
		for range 2 {
			cfg, err := buildConfig(local, node, nil, kernel.TLSCert{})
			if err != nil {
				t.Fatal(err)
			}
			if item.expected == "disabled" {
				if cfg["log"].(M)["disabled"] != true {
					t.Fatal("日志未禁用")
				}
				continue
			}
			if got := cfg["log"].(M)["level"]; got != item.expected {
				t.Fatalf("日志等级=%v，预期 %s", got, item.expected)
			}
		}
	}
	node.KernelLogLevel = "invalid"
	if _, err := buildConfig(local, node, nil, kernel.TLSCert{}); err == nil {
		t.Fatal("无效日志等级通过生成")
	}
}

func TestPanelLogLevelControlsActualSingBoxLogger(t *testing.T) {
	for _, level := range []string{"debug", "warn", "none"} {
		node := &model.NodeSpec{Protocol: "vless", ServerPort: 10001, KernelLogLevel: level}
		cfg, err := buildConfig(config.KernelConfig{LogLevel: "warn"}, node, nil, kernel.TLSCert{})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(cfg["log"])
		var options option.LogOptions
		if err := json.Unmarshal(data, &options); err != nil {
			t.Fatal(err)
		}
		// 输出改到测试缓冲；沿用实际生成的等级与禁用设置。
		options.Output = ""
		var output bytes.Buffer
		factory, err := blog.New(blog.Options{Context: t.Context(), Options: options, DefaultWriter: &output})
		if err != nil {
			t.Fatal(err)
		}
		if err := factory.Start(); err != nil {
			t.Fatal(err)
		}
		factory.Logger().Debug("review-debug-marker")
		factory.Close()
		if strings.Contains(output.String(), "review-debug-marker") != (level == "debug") {
			t.Fatalf("实际核心未按面板等级 %s 输出", level)
		}
	}
}
