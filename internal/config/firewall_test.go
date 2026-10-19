package config

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirewallConfigInheritanceAndSharedState(t *testing.T) {
	t.Setenv("YZ_FIREWALL_TEST_TOKEN", rand.Text())
	for _, settings := range []string{"", "firewall:\n  enabled: false\n  backend: ufw\n  redirect_backend: iptables\n  source_backend: iptables\n  state_dir: state/firewall\n"} {
		path := writeTemp(t, settings+`
instances:
  - panel:
      url: https://panel.example.invalid
      token_env: YZ_FIREWALL_TEST_TOKEN
      node_id: 1
  - panel:
      url: https://panel.example.invalid
      token_env: YZ_FIREWALL_TEST_TOKEN
      node_id: 2
`)
		root, err := LoadRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		first, second := root.Instances[0], root.Instances[1]
		if !first.Firewall.Equal(second.Firewall) || first.Firewall.IsEnabled() != (settings == "") {
			t.Fatal("进程共享防火墙配置继承错误")
		}
		if settings != "" && first.Firewall.SourceBackend != "iptables" {
			t.Fatal("来源过滤后端没有继承")
		}
		want := filepath.Join(filepath.Dir(path), "firewall")
		if settings != "" {
			want = filepath.Join(filepath.Dir(path), "state", "firewall")
		}
		if first.Firewall.StateDir != want || first.Kernel.ConfigDir == second.Kernel.ConfigDir {
			t.Fatal("防火墙归属应共享，内核运行目录应保持独立")
		}
	}
}

func TestFirewallConfigRejectsConflictingInstances(t *testing.T) {
	t.Setenv("YZ_FIREWALL_TEST_TOKEN", rand.Text())
	path := writeTemp(t, `
instances:
  - panel:
      url: https://panel.example.invalid
      token_env: YZ_FIREWALL_TEST_TOKEN
      node_id: 1
    firewall:
      backend: ufw
  - panel:
      url: https://panel.example.invalid
      token_env: YZ_FIREWALL_TEST_TOKEN
      node_id: 2
    firewall:
      backend: firewalld
`)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateStartupLayout([]*Config{&root.Instances[0], &root.Instances[1]})
	if err == nil || !strings.Contains(err.Error(), "firewall") {
		t.Fatalf("没有拒绝同进程冲突配置：%v", err)
	}
}

func TestFirewallConfigRejectsInvalidBackendsAndZones(t *testing.T) {
	for _, cfg := range []FirewallConfig{
		{Backend: "iptables"}, {RedirectBackend: "ufw"}, {SourceBackend: "ufw"}, {Zone: "public --permanent"}, {Zone: "../public"},
	} {
		t.Run(fmt.Sprint(cfg), func(t *testing.T) {
			if err := ValidateFirewall(cfg); err == nil {
				t.Fatal("未拒绝非法后端或区域")
			}
		})
	}
}
