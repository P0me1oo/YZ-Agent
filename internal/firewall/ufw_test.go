package firewall

import (
	"context"
	"errors"
	"strconv"
	"strings"
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
	if len(rules) != 4 || rules[0].Rule.Family != 6 || rules[0].Rule.Ports.To != 20100 || rules[1].Rule.Address != "192.0.2.10" || !rules[2].ScopedSource || !rules[3].ScopedSource {
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

func TestUFWDetectsOverlappingBlockingRulesBeforeAnyWrite(t *testing.T) {
	for _, action := range []string{"DENY", "REJECT", "LIMIT"} {
		for _, protocol := range []string{"tcp", "udp"} {
			for _, family := range []int{4, 6} {
				t.Run(action+"/"+protocol+"/"+strconv.Itoa(family), func(t *testing.T) {
					spy := &commandSpy{}
					b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", spy)
					if err != nil {
						t.Fatal(err)
					}
					defer b.Close()
					from := "Anywhere"
					if family == 6 {
						from += " (v6)"
					}
					// 先允许一个来源、再拒绝所有来源，末尾已有自动 ALLOW 仍不能绕过拒绝。
					existing := parseUFWStatus("[ 1] 43222 ALLOW IN 198.51.100.7\n" +
						"[ 2] 43222 " + action + " IN " + from + "\n" +
						"[ 3] 43222/" + protocol + " ALLOW IN " + from + " # manual\n")
					rule := ownedRule{Backend: "ufw", Rule: Rule{Family: family, Protocol: protocol, Ports: portset.Range{From: 43222, To: 43222}}}
					err = b.ensureUFW(context.Background(), rule, existing)
					if err == nil || !strings.Contains(err.Error(), action) || len(spy.calls) != 0 || len(b.state.Owned) != 0 {
						t.Fatalf("应保留手工规则并报告协议通配冲突: %v", err)
					}
				})
			}
		}
	}
}

func TestUFWBlockingRuleOverlapRespectsPortAddressFamilyAndProtocol(t *testing.T) {
	wanted := Rule{Family: 4, Address: "192.0.2.1", Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}}
	for _, test := range []struct {
		name, line string
		conflict   bool
	}{
		{"range", "[ 1] 43000:44000/tcp DENY IN Anywhere", true},
		{"scoped source", "[ 1] 43222/tcp REJECT IN 198.51.100.0/24", true},
		{"interface", "[ 1] 43222/tcp on eth0 DENY IN Anywhere", true},
		{"other port", "[ 1] 43223 DENY IN Anywhere", false},
		{"other family", "[ 1] 43222 DENY IN Anywhere (v6)", false},
		{"other protocol", "[ 1] 43222/udp DENY IN Anywhere", false},
		{"other address", "[ 1] 192.0.2.2 43222/tcp DENY IN Anywhere", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkUFWBlockingRules([]Rule{wanted}, parseUFWStatus(test.line))
			if (err != nil) != test.conflict {
				t.Fatalf("冲突结果不符: %v", err)
			}
		})
	}
}

type blockingUFW struct {
	status string
	writes int
}

func (*blockingUFW) Available(name string) bool { return name == "ufw" }

func (r *blockingUFW) Run(_ context.Context, _, _ string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "status" {
		return r.status, nil
	}
	r.writes++
	return "", nil
}

func TestUFWManualRestrictionSkipsOnlyItsPortAndRecovers(t *testing.T) {
	runner := &blockingUFW{status: "Status: active\n[ 1] 43222 ALLOW IN 198.51.100.7\n[ 2] 43222 DENY IN Anywhere\n"}
	b, err := newSystemBackend(config.FirewallConfig{StateDir: t.TempDir()}, "test-owner", runner)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	allows := []Rule{
		{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 10721, To: 10721}},
		{Family: 4, Protocol: "tcp", Ports: portset.Range{From: 43222, To: 43222}},
	}
	if err := b.Apply(context.Background(), allows, nil); err != nil || runner.writes != 1 || len(b.state.Owned) != 1 || b.state.Owned[0].Rule.Ports.From != 10721 {
		t.Fatalf("应保留手工限制并只放行其他端口: %v", err)
	}
	runner.status = "Status: active\n"
	if err := b.Apply(context.Background(), allows, nil); err != nil || runner.writes != 3 {
		t.Fatalf("解除冲突后应正常重试: %v", err)
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
