package firewall

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

type ownedRule struct {
	Backend string `json:"backend"`
	Zone    string `json:"zone,omitempty"`
	Rule    Rule   `json:"rule"`
}

type savedState struct {
	Version          int         `json:"version"`
	Scope            string      `json:"scope"`
	Namespace        string      `json:"namespace,omitempty"`
	Owned            []ownedRule `json:"owned,omitempty"`
	Redirect         string      `json:"redirect_backend,omitempty"`
	RedirectFamilies []int       `json:"redirect_families,omitempty"`
}

type systemBackend struct {
	cfg      config.FirewallConfig
	scope    string
	commands commands
	path     string
	lock     *os.File
	state    savedState
}

func newSystemBackend(cfg config.FirewallConfig, scope string, runner commands) (*systemBackend, error) {
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("防火墙状态目录不能为空")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建防火墙状态目录: %w", err)
	}
	path := filepath.Join(cfg.StateDir, scope+".json")
	lock, err := lockState(filepath.Join(cfg.StateDir, scope+".lock"))
	if err != nil {
		return nil, err
	}
	b := &systemBackend{cfg: cfg, scope: scope, commands: runner, path: path, lock: lock,
		state: savedState{Version: 1, Scope: scope, Namespace: "yz-agent"}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err == nil {
		b.state = savedState{}
		err = json.Unmarshal(data, &b.state)
	}
	if err == nil {
		err = b.validateState()
	}
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("读取防火墙归属记录: %w", err)
	}
	return b, nil
}

func (b *systemBackend) validateState() error {
	if b.state.Version != 1 || b.state.Scope != b.scope {
		return fmt.Errorf("防火墙状态版本或实例标识不匹配")
	}
	if b.state.Namespace != "" && b.state.Namespace != "yz-agent" {
		return fmt.Errorf("未知的防火墙规则名称空间")
	}
	if b.state.Redirect != "" && b.state.Redirect != "nftables" && b.state.Redirect != "iptables" {
		return fmt.Errorf("无效的端口转发状态")
	}
	for _, family := range b.state.RedirectFamilies {
		if family != 4 && family != 6 {
			return fmt.Errorf("无效的端口转发地址族")
		}
	}
	for _, owned := range b.state.Owned {
		if (owned.Backend != "ufw" && owned.Backend != "firewalld") || !owned.Rule.valid() || owned.Rule.Target != 0 {
			return fmt.Errorf("无效的防火墙规则归属记录")
		}
		if err := config.ValidateFirewall(config.FirewallConfig{Zone: owned.Zone}); err != nil {
			return err
		}
		if owned.Backend == "firewalld" && owned.Zone == "" {
			return fmt.Errorf("firewalld 归属记录缺少区域")
		}
	}
	return nil
}

// save 先记录创建意图，再执行规则变更；意外退出后仍能精确回收本实例规则。
func (b *systemBackend) save() error {
	data, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(b.path), ".firewall-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, b.path)
	}
	if err == nil {
		err = syncStateDirectory(filepath.Dir(b.path))
	}
	return err
}

func (b *systemBackend) hasOwned(rule ownedRule) bool {
	for _, existing := range b.state.Owned {
		if existing == rule {
			return true
		}
	}
	return false
}

func (b *systemBackend) remember(rule ownedRule) error {
	if b.hasOwned(rule) {
		return nil
	}
	b.state.Owned = append(b.state.Owned, rule)
	if err := b.save(); err != nil {
		b.state.Owned = b.state.Owned[:len(b.state.Owned)-1]
		return err
	}
	return nil
}

func (b *systemBackend) forget(rule ownedRule) error {
	for i, existing := range b.state.Owned {
		if existing == rule {
			previous := b.state.Owned
			b.state.Owned = append(append([]ownedRule(nil), previous[:i]...), previous[i+1:]...)
			if err := b.save(); err != nil {
				b.state.Owned = previous
				return err
			}
			return nil
		}
	}
	return nil
}

func (b *systemBackend) Close() error {
	if b.lock == nil {
		return nil
	}
	err := b.lock.Close()
	b.lock = nil
	return err
}
