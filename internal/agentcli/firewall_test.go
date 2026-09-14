package agentcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"gopkg.in/yaml.v3"
)

func TestWriteRootConfigPreservesFirewall(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		fw := config.FirewallConfig{Enabled: &enabled, Backend: "firewalld", RedirectBackend: "iptables", Zone: "public", StateDir: "state/firewall"}
		root := &config.RootConfig{
			Config:    config.Config{Firewall: fw},
			Instances: []config.Config{{Firewall: fw, Panel: config.PanelConfig{URL: "https://panel.example.invalid", TokenEnv: "YZ_FIREWALL_TEST_TOKEN", NodeID: 1}}},
		}
		path := filepath.Join(t.TempDir(), "config.yml")
		if err := writeRootConfig(path, root); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip config.RootConfig
		if err := yaml.Unmarshal(data, &roundtrip); err != nil {
			t.Fatal(err)
		}
		if len(roundtrip.Instances) != 1 || !roundtrip.Firewall.Equal(fw) || !roundtrip.Instances[0].Firewall.Equal(fw) {
			t.Fatal("xbctl 写配置后丢失防火墙开关、后端、区域或归属目录")
		}
	}
}
