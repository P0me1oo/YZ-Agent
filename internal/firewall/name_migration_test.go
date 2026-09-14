package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type migrationCommands struct {
	driver  string
	scope   string
	comment string
	exists  bool
	jump    bool
	fail    bool
	calls   []string
}

func (c *migrationCommands) Available(name string) bool {
	switch c.driver {
	case "ufw":
		return name == "ufw"
	case "nftables":
		return name == "nft"
	case "iptables":
		return name == "iptables" || name == "iptables-restore"
	}
	return false
}

func (c *migrationCommands) Run(_ context.Context, name, input string, args ...string) (string, error) {
	c.calls = append(c.calls, name+" "+strings.Join(args, " ")+" "+input)
	remove := func() (string, error) {
		if c.fail {
			return "", errors.New("模拟旧规则清理失败")
		}
		c.exists = false
		return "", nil
	}
	switch name {
	case "ufw":
		if args[0] == "status" {
			status := "Status: active\n[ 1] 9443/tcp ALLOW IN Anywhere # manual-fixture\n"
			if c.exists {
				status += "[ 2] 8443/udp ALLOW IN Anywhere # " + c.comment + "\n"
			}
			return status, nil
		}
		if args[0] != "--force" || args[len(args)-1] != c.comment {
			return "", errors.New("触及非本实例旧规则")
		}
		return remove()
	case "nft":
		if args[0] == "--json" {
			if !c.exists {
				return "No such file or directory: yz_node_" + c.scope, errors.New("表不存在")
			}
			return fmt.Sprintf(`{"nftables":[{"table":{"family":"inet","name":"yz_node_%s"}},{"chain":{"name":"ownership"}},{"rule":{"chain":"ownership","comment":"yzboard-node:%s"}},{"chain":{"name":"prerouting","type":"nat","hook":"prerouting","prio":-100,"policy":"accept"}}]}`, c.scope, c.scope), nil
		}
		if strings.TrimSpace(input) != "delete table inet yz_node_"+c.scope {
			return "", errors.New("删除了非本实例 nftables 表")
		}
		return remove()
	case "iptables":
		chain := "YZH_" + c.scope
		switch args[4] {
		case "-S":
			if c.exists {
				return "-N " + chain + "\n-A " + chain + " -m comment --comment yzboard-node:" + c.scope + " -j RETURN\n", nil
			}
			return "", nil
		case "-C":
			if c.jump {
				return "", nil
			}
			return "Bad rule", errors.New("跳转不存在")
		case "-D":
			c.jump = false
			return "", nil
		}
	case "iptables-restore":
		if !strings.Contains(input, "-X YZH_"+c.scope+"\n") {
			return "", errors.New("删除了非本实例 iptables 链")
		}
		return remove()
	}
	return "", fmt.Errorf("非预期命令: %s", name)
}

func TestFirewallMigratesLegacyNamesAfterSuccessfulCleanup(t *testing.T) {
	for _, driver := range []string{"ufw", "nftables", "iptables"} {
		for _, failFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%v", driver, failFirst), func(t *testing.T) {
				dir, scope := t.TempDir(), "a1b2c3d4e5f6"
				rule := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}
				legacy := savedState{Version: 1, Scope: scope}
				if driver == "ufw" {
					legacy.Owned = []ownedRule{{Backend: "ufw", Rule: rule}}
				} else {
					legacy.Redirect, legacy.RedirectFamilies = driver, []int{4}
				}
				data, err := json.Marshal(legacy)
				if err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(dir, scope+".json")
				if err := os.WriteFile(statePath, data, 0o600); err != nil {
					t.Fatal(err)
				}
				original := &systemBackend{scope: scope, state: legacy}
				commands := &migrationCommands{driver: driver, scope: scope, comment: original.ufwComment(rule), exists: true, jump: true, fail: failFirst}
				cfg := config.FirewallConfig{Backend: "none", StateDir: dir}
				backend, err := newSystemBackend(cfg, scope, commands)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { backend.Close() }()
				if err := backend.Apply(context.Background(), []Rule{{}}, nil); err == nil || len(commands.calls) != 0 {
					t.Fatal("无效规则不应触发迁移")
				}
				err = backend.Apply(context.Background(), nil, nil)
				if failFirst {
					if err == nil || backend.state.Namespace != "" {
						t.Fatal("清理失败后不能切换名称")
					}
					backend.Close()
					backend, err = newSystemBackend(cfg, scope, commands)
					if err != nil {
						t.Fatal(err)
					}
					if backend.state.Namespace != "" {
						t.Fatal("失败状态未保留供重启恢复")
					}
					commands.fail = false
					err = backend.Apply(context.Background(), nil, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				if commands.exists || backend.state.Namespace != "yz-agent" || len(backend.state.Owned) != 0 || backend.state.Redirect != "" {
					t.Fatal("旧规则没有清理完成")
				}
				if backend.nftTable() != "yz_agent_"+scope || backend.iptablesChain() != "YZ_AGENT_"+scope || !strings.HasPrefix(backend.ufwComment(rule), "yz-agent:") {
					t.Fatal("新规则未使用正式名称")
				}
				backend.Close()
				backend, err = newSystemBackend(cfg, scope, commands)
				if err != nil {
					t.Fatal(err)
				}
				calls := len(commands.calls)
				if err := backend.Apply(context.Background(), nil, nil); err != nil {
					t.Fatal(err)
				}
				if backend.state.Namespace != "yz-agent" || len(commands.calls) != calls {
					t.Fatal("重复启动再次迁移了旧规则")
				}
			})
		}
	}
}

func TestUFWRecognizesOtherInstancesWithEitherName(t *testing.T) {
	for _, prefix := range []string{"yz-agent:", "yzboard-node:"} {
		spy := &commandSpy{}
		backend, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "fixture-owner", spy)
		if err != nil {
			t.Fatal(err)
		}
		rule := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}
		err = backend.ensureUFW(context.Background(), ownedRule{Backend: "ufw", Rule: rule}, []ufwRule{{Rule: rule, Action: "ALLOW", Comment: prefix + "other-instance"}})
		backend.Close()
		if err == nil || len(spy.calls) != 0 {
			t.Fatal("不能覆盖其他实例的托管规则")
		}
	}
}
