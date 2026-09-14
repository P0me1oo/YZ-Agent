package firewall

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type inactiveFirewalldCommands struct{ output string }

func (inactiveFirewalldCommands) Available(name string) bool {
	return name == "ufw" || name == "firewall-cmd"
}

func (c inactiveFirewalldCommands) Run(_ context.Context, name, _ string, args ...string) (string, error) {
	if name == "firewall-cmd" {
		return c.output, errors.New("firewall-cmd 状态检查失败")
	}
	if len(args) > 0 && args[0] == "status" {
		return "Status: active\n", nil
	}
	return "Rule added\n", nil
}

func TestAutoFirewallHandlesInstalledButInactiveFirewalld(t *testing.T) {
	for _, cause := range []string{"No such file or directory", "Connection refused", "Permission denied"} {
		t.Run(cause, func(t *testing.T) {
			runner := inactiveFirewalldCommands{output: "Error: DBUS_ERROR: Failed to connect to socket /run/dbus/system_bus_socket: " + cause}
			b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", runner)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			err = b.Apply(context.Background(), []Rule{{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 8443, To: 8443}}}, nil)
			if cause == "Permission denied" {
				if err == nil {
					t.Fatal("不能把权限错误当作未运行")
				}
			} else if err != nil || len(b.state.Owned) != 1 || b.state.Owned[0].Backend != "ufw" {
				t.Fatalf("未运行的 firewalld 不应阻止 UFW 管理：%v", err)
			}
		})
	}
}
