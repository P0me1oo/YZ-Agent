package xray

import (
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	appLog "github.com/xtls/xray-core/app/log"
	clog "github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/infra/conf"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPanelLogLevelAndClearedOverrideReachXrayConfiguration(t *testing.T) {
	local := config.KernelConfig{LogLevel: "warn"}
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 10001}
	for _, item := range []struct{ remote, expected string }{{"debug", "debug"}, {"info", "info"}, {"", "warning"}, {"none", "none"}} {
		node.KernelLogLevel = item.remote
		for range 2 {
			cfg := buildConfig(local, node, nil, kernel.TLSCert{})
			if got := cfg["log"].(M)["loglevel"]; got != item.expected {
				t.Fatalf("日志等级=%v，预期 %s", got, item.expected)
			}
		}
	}
}

func TestPanelLogLevelControlsActualXrayLogger(t *testing.T) {
	for _, level := range []string{"debug", "warn", "none"} {
		path := filepath.Join(t.TempDir(), "core.log")
		node := &model.NodeSpec{Protocol: "vless", ServerPort: 10001, KernelLogLevel: level}
		cfg := buildConfig(config.KernelConfig{LogLevel: "warn"}, node, nil, kernel.TLSCert{})
		cfg["log"].(M)["error"] = path
		cfg["log"].(M)["access"] = "none"
		data, _ := json.Marshal(cfg["log"])
		var native conf.LogConfig
		if err := json.Unmarshal(data, &native); err != nil {
			t.Fatal(err)
		}
		logger, err := appLog.New(t.Context(), native.Build())
		if err != nil {
			t.Fatal(err)
		}
		logger.Handle(&clog.GeneralMessage{Severity: clog.Severity_Debug, Content: "review-debug-marker"})
		if level == "debug" {
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(path)
				if strings.Contains(string(data), "review-debug-marker") {
					break
				}
				time.Sleep(time.Millisecond)
			}
		}
		logger.Close()
		output, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(output), "review-debug-marker") != (level == "debug") {
			t.Fatalf("实际核心未按面板等级 %s 输出", level)
		}
	}
}
