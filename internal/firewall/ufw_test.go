package firewall

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/portset"
)

type commandSpy struct {
	calls [][]string
	err   error
}

func (c *commandSpy) Available(string) bool { return true }
func (c *commandSpy) Run(_ context.Context, name, input string, args ...string) (string, error) {
	c.calls = append(c.calls, append([]string{name, input}, args...))
	return "", c.err
}

func TestUFWPreservesExistingManualRuleAndItsComment(t *testing.T) {
	ctx := context.Background()
	spy := &commandSpy{}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", spy)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	rule := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}
	existing := parseUFWStatus("Status: active\n[ 1] 8443/udp                 ALLOW IN    Anywhere                   # manual rule\n")
	if err := b.ensureUFW(ctx, ownedRule{Backend: "ufw", Rule: rule}, existing); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 0 || len(b.state.Owned) != 0 {
		t.Fatal("已有手工规则不能改写或认领")
	}
}

func TestUFWRecognizesScopedIPv6Rules(t *testing.T) {
	output := "[ 1] 20000:20100/udp (v6) ALLOW IN Anywhere (v6) # owner\n" +
		"[ 2] 192.0.2.10 8443/tcp ALLOW IN Anywhere # scoped\n" +
		"[ 3] 8443/tcp on eth0 ALLOW IN Anywhere # interface rule\n" +
		"[ 4] 8443/tcp ALLOW IN 192.0.2.0/24 # restricted source\n"
	rules := parseUFWStatus(output)
	if len(rules) != 2 || rules[0].Rule.Family != 6 || rules[0].Rule.Ports.To != 20100 || rules[1].Rule.Address != "192.0.2.10" {
		t.Fatalf("不能准确识别规则范围：%+v", rules)
	}
}

func TestUFWPreservesDenyRejectLimitAndLoggedRules(t *testing.T) {
	for _, action := range []string{"DENY", "REJECT", "LIMIT", "ALLOW"} {
		t.Run(action, func(t *testing.T) {
			spy := &commandSpy{}
			b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", spy)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			rule := Rule{Family: 6, Address: "2001:db8::1", Protocol: "tcp", Ports: portset.Range{From: 8443, To: 8443}}
			existing := parseUFWStatus("[ 1] 2001:db8::1 8443/tcp " + action + " IN Anywhere (v6) (log-all) # manual\n")
			if len(existing) != 1 || existing[0].Rule != rule {
				t.Fatalf("未识别带日志或指定 IPv6 的规则：%+v", existing)
			}
			err = b.ensureUFW(context.Background(), ownedRule{Backend: "ufw", Rule: rule}, existing)
			if (err != nil) != (action != "ALLOW") || len(spy.calls) != 0 || len(b.state.Owned) != 0 {
				t.Fatalf("应保留手工规则并报告非放行冲突：%v", err)
			}
		})
	}
}

func TestUFWInactiveCleanupChecksEntireRule(t *testing.T) {
	rule := Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 20000, To: 20100}}
	for _, line := range []string{
		"ufw allow 20000:20100/udp comment 'owner'",
		"ufw allow log to any port 20000:20100 proto udp comment 'owner'",
	} {
		if !ufwAddedOwns(line, rule, "owner") {
			t.Fatalf("未识别自己的持久规则：%s", line)
		}
	}
	for _, line := range []string{
		"ufw deny 20000:20100/udp comment 'owner'",
		"ufw allow out 20000:20100/udp comment 'owner'",
		"ufw allow from 192.0.2.0/24 to any port 20000:20100 proto udp comment 'owner'",
		"ufw allow from any port 8080 to any port 20000:20100 proto udp comment 'owner'",
		"ufw allow 20000:20101/udp comment 'owner'",
		"ufw allow 20000:20100/udp comment 'manual'",
	} {
		if ufwAddedOwns(line, rule, "owner") {
			t.Fatalf("不能删除管理员替换后的规则：%s", line)
		}
	}
}

func TestUFWFailedCreationRetainsRecoverableOwnership(t *testing.T) {
	ctx := context.Background()
	spy := &commandSpy{err: errors.New("命令超时")}
	dir := t.TempDir()
	b, err := newSystemBackend(config.FirewallConfig{StateDir: dir}, "test-owner", spy)
	if err != nil {
		t.Fatal(err)
	}
	rule := ownedRule{Backend: "ufw", Rule: Rule{Family: 4, Protocol: "udp", Ports: portset.Range{From: 8443, To: 8443}}}
	if err := b.ensureUFW(ctx, rule, nil); err == nil {
		t.Fatal("创建失败应返回错误")
	}
	b.Close()
	reopened, err := newSystemBackend(config.FirewallConfig{StateDir: dir}, "test-owner", spy)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.hasOwned(rule) {
		t.Fatal("进程重启后丢失创建意图，无法清理可能已生效的规则")
	}
	spy.err, spy.calls = nil, nil
	// 管理员已换成自己的规则时不能删除，仅清除旧归属记录。
	if err := reopened.removeUFW(ctx, rule, []ufwRule{{Rule: rule.Rule, Comment: "manual replacement"}}, ""); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 0 || reopened.hasOwned(rule) {
		t.Fatal("不能删除不再带有本实例标记的规则")
	}
}
