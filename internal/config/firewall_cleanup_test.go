package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFirewallWithoutRuntimeCredentials(t *testing.T) {
	for _, body := range []string{
		"firewall:\n  state_dir: state\n  backend: ufw\npanel:\n  token_env: ABSENT_TEST_CREDENTIAL\n",
		"firewall:\n  state_dir: state\n  backend: ufw\ninstances:\n  - machine:\n      token_env: ABSENT_TEST_CREDENTIAL\n  - panel:\n      token_env: ABSENT_TEST_CREDENTIAL\n",
	} {
		dir := t.TempDir()
		name := filepath.Join(dir, "config.yml")
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFirewall(name)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Backend != "ufw" || cfg.StateDir != filepath.Join(dir, "state") {
			t.Fatalf("清理配置没有沿用实例的状态目录: %+v", cfg)
		}
	}
}

func TestLoadFirewallRejectsMissingInvalidOrConflictingConfig(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "config.yml")
	if _, err := LoadFirewall(name); err == nil {
		t.Fatal("配置缺失不能改为清理默认目录")
	}
	for _, body := range []string{
		"firewall: [broken",
		"firewall:\n  backend: invalid\n",
		"instances:\n  - firewall:\n      state_dir: first\n  - firewall:\n      state_dir: second\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadFirewall(name); err == nil {
			t.Fatal("无效或冲突的清理配置必须拒绝")
		}
	}
}
