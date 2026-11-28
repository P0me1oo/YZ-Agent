package agentcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"gopkg.in/yaml.v3"
)

func TestConfigInitSavesHTTPConfirmationPerInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	args := []string{
		"--mode", "machine", "--machine-id", "5", "--panel-url", "HTTP://PANEL.EXAMPLE.INVALID:8080/",
		"--config", path, "--output", path, "--http-confirmed",
	}
	if err := runConfigInit(args); err != nil {
		t.Fatal(err)
	}
	// 其他配置维护不应遗失已确认的入口。
	if err := runConfigInit(args[:len(args)-1]); err != nil {
		t.Fatal(err)
	}
	if err := runConfigInit([]string{
		"--mode", "machine", "--machine-id", "6", "--panel-url", "https://other.example.invalid",
		"--config", path, "--output", path,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root config.RootConfig
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Instances) != 2 ||
		root.Instances[0].Panel.HTTPConfirmedURL != "http://panel.example.invalid:8080" ||
		root.Instances[1].Panel.HTTPConfirmedURL != "" {
		t.Fatal("HTTP 确认丢失或扩散到其他实例")
	}
}

func TestConfigInitHTTPConfirmationCannotDisableHTTPSValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	err := runConfigInit([]string{
		"--mode", "machine", "--machine-id", "5", "--panel-url", "https://panel.example.invalid",
		"--output", path, "--http-confirmed",
	})
	if err == nil {
		t.Fatal("HTTP 确认不能用于 HTTPS")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("无效确认不应写入配置")
	}
	if err := runConfigInit([]string{"--supports-http-confirmation"}); err != nil {
		t.Fatal(err)
	}
}
