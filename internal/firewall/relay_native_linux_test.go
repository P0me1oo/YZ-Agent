//go:build linux

package firewall

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type relayNativeCommands struct {
	nativeCommands
	failReadback, wrote bool
}

func (c *relayNativeCommands) Run(ctx context.Context, name, input string, args ...string) (string, error) {
	if c.failReadback && c.wrote && name == "nft" && strings.Join(args, " ") == "--json list tables" {
		c.failReadback = false
		return "", errors.New("simulated readback failure")
	}
	if c.failReadback && c.wrote && name == "ip6tables" {
		c.failReadback = false
		return "", errors.New("simulated IPv6 readback failure")
	}
	output, err := c.nativeCommands.Run(ctx, name, input, args...)
	if input != "" && name == "nft" && err == nil {
		c.wrote = true
	}
	if input != "" && name == "ip6tables-restore" && err == nil {
		c.wrote = true
	}
	return output, err
}

func TestNativeRelayFirewallDualStackAndRollback(t *testing.T) {
	requireNativeFirewall(t)
	ctx := context.Background()
	runner := &relayNativeCommands{nativeCommands: nativeCommands{t: t}}
	driver := os.Getenv("YZ_RELAY_DRIVER")
	if driver == "" {
		driver = "nftables"
	}
	cfg := config.FirewallConfig{Backend: "none", SourceBackend: driver, StateDir: t.TempDir()}
	b, err := newSystemBackend(cfg, "relay-native", runner)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(b)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	nativeEcho(t, 28388)
	nativeEcho(t, 28389)
	nativeCommand(t, "nft", "add", "table", "inet", "manual_fixture")
	nativeCommand(t, "nft", "add", "chain", "inet", "manual_fixture", "input", "{ type filter hook input priority 0; policy accept; }")
	nativeCommand(t, "nft", "add", "rule", "inet", "manual_fixture", "input", "tcp", "dport", "28389", "accept")
	manual := nativeCommand(t, "nft", "-j", "list", "table", "inet", "manual_fixture")
	node := verifiedLanding()
	node.Relay.Firewall.Sources = []string{"192.0.2.2", "2001:db8:1::2"}
	apply := func() {
		t.Helper()
		if err := m.Apply(ctx, "landing", node, "xray"); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(v4, v6 bool) {
		t.Helper()
		for _, protocol := range []string{"tcp", "udp"} {
			probeNative(t, nativeProbe{Protocol: protocol, Address: "192.0.2.1", Port: 28388, Open: v4},
				nativeProbe{Protocol: protocol, Address: "2001:db8:1::1", Port: 28388, Open: v6})
		}
	}
	apply()
	apply()
	probe(true, true)
	node.Relay.Firewall.Sources = []string{"192.0.2.9"}
	runner.failReadback, runner.wrote = true, false
	if err := m.Apply(ctx, "landing", node, "xray"); err == nil || !strings.Contains(err.Error(), "已恢复原规则") {
		t.Fatal("rollback not reported", err)
	}
	probe(true, true)
	if driver == "nftables" {
		path := b.path
		b.path = cfg.StateDir // 目录不能被归属文件替换，模拟写盘失败。
		err := m.Apply(ctx, "landing", node, "xray")
		b.path = path
		if err == nil || !strings.Contains(err.Error(), "已恢复原规则") {
			t.Fatal("state save rollback not reported", err)
		}
		probe(true, true)
	}
	apply()
	probe(false, false)
	node.Relay.Firewall.Status = "pending"
	if err := m.Apply(ctx, "landing", node, "xray"); err == nil {
		t.Fatal("pending accepted")
	}
	probe(false, false)
	// 模拟异常退出后重启：磁盘归属记录保留，未知来源不能被其他节点同步清掉。
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = newSystemBackend(cfg, "relay-native", runner)
	if err != nil {
		t.Fatal(err)
	}
	m = testManager(b)
	if err := m.Apply(ctx, "landing", node, "xray"); err == nil {
		t.Fatal("pending restart accepted")
	}
	other := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 28389}
	if err := m.Apply(ctx, "other", other, "singbox"); err != nil {
		t.Fatal(err)
	}
	probe(false, false)
	probeNative(t, nativeProbe{Protocol: "tcp", Address: "192.0.2.1", Port: 28389, Open: true},
		nativeProbe{Protocol: "udp", Address: "2001:db8:1::1", Port: 28389, Open: true})
	if got := nativeCommand(t, "nft", "-j", "list", "table", "inet", "manual_fixture"); got != manual {
		t.Fatal("manual table changed")
	}
	if err := m.Release(ctx, "landing"); err != nil {
		t.Fatal(err)
	}
	probe(true, true)
}
