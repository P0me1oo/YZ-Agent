package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FirewallConfig 在同一进程内共享，避免多个节点互相回收端口规则。
type FirewallConfig struct {
	Enabled         *bool  `yaml:"enabled,omitempty"`
	Backend         string `yaml:"backend,omitempty"`
	RedirectBackend string `yaml:"redirect_backend,omitempty"`
	Zone            string `yaml:"zone,omitempty"`
	StateDir        string `yaml:"state_dir,omitempty"`
}

// LoadFirewall 只读取规则清理所需配置，不要求加载面板凭据或启动内核。
func LoadFirewall(name string) (FirewallConfig, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return FirewallConfig{}, err
	}
	var root RootConfig
	if err := yaml.Unmarshal(data, &root); err != nil {
		return FirewallConfig{}, err
	}
	baseDir := configBaseDir(name)
	result := root.Firewall
	if len(root.Instances) == 0 {
		result.setDefaults(baseDir)
		return result, ValidateFirewall(result)
	}
	for index, instance := range root.Instances {
		current := instance.Firewall
		current.inheritFrom(root.Firewall)
		current.setDefaults(baseDir)
		if err := ValidateFirewall(current); err != nil {
			return FirewallConfig{}, err
		}
		if index == 0 {
			result = current
		} else if !result.Equal(current) {
			return FirewallConfig{}, fmt.Errorf("实例的防火墙配置不一致，停止清理")
		}
	}
	return result, nil
}

func (c FirewallConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c FirewallConfig) Equal(other FirewallConfig) bool {
	return c.IsEnabled() == other.IsEnabled() && c.Backend == other.Backend &&
		c.RedirectBackend == other.RedirectBackend && c.Zone == other.Zone && c.StateDir == other.StateDir
}

func (c *FirewallConfig) inheritFrom(parent FirewallConfig) {
	if c.Enabled == nil && parent.Enabled != nil {
		enabled := *parent.Enabled
		c.Enabled = &enabled
	}
	if c.Backend == "" {
		c.Backend = parent.Backend
	}
	if c.RedirectBackend == "" {
		c.RedirectBackend = parent.RedirectBackend
	}
	if c.Zone == "" {
		c.Zone = parent.Zone
	}
	if c.StateDir == "" {
		c.StateDir = parent.StateDir
	}
}

func (c *FirewallConfig) setDefaults(baseDir string) {
	if c.Backend == "" {
		c.Backend = "auto"
	}
	if c.RedirectBackend == "" {
		c.RedirectBackend = "auto"
	}
	if c.StateDir == "" {
		c.StateDir = filepath.Join(baseDir, "firewall")
	}
	if !filepath.IsAbs(c.StateDir) {
		c.StateDir = filepath.Join(baseDir, c.StateDir)
	}
	c.StateDir = filepath.Clean(c.StateDir)
}

func ValidateFirewall(c FirewallConfig) error {
	switch c.Backend {
	case "", "auto", "ufw", "firewalld", "none":
	default:
		return fmt.Errorf("firewall.backend 必须为 auto、ufw、firewalld 或 none")
	}
	switch c.RedirectBackend {
	case "", "auto", "nftables", "iptables":
	default:
		return fmt.Errorf("firewall.redirect_backend 必须为 auto、nftables 或 iptables")
	}
	for _, r := range c.Zone {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return fmt.Errorf("firewall.zone 包含无效字符")
		}
	}
	return nil
}
