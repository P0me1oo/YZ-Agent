//go:build linux

package firewall

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

func TestNativeSourceFirewallLifecycle(t *testing.T) {
	requireNativeFirewall(t)
	backend, driver := os.Getenv("YZ_FIREWALL_BACKEND"), os.Getenv("YZ_FIREWALL_REDIRECT")
	if (backend != "ufw" && backend != "firewalld") || (driver != "nftables" && driver != "iptables") {
		t.Fatal("必须指定隔离防火墙测试组合")
	}
	ctx := context.Background()
	cfg := config.FirewallConfig{Backend: backend, SourceBackend: driver, StateDir: t.TempDir()}
	b, err := newSystemBackend(cfg, "source-native", nativeCommands{t})
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(b)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	nativeEcho(t, 28453)
	nativeEcho(t, 28454)
	// 已有公开放行不能绕过来源过滤，其他端口的手工规则须完整保留。
	if backend == "ufw" {
		nativeCommand(t, "ufw", "allow", "28453", "comment", "manual-source-fixture")
		nativeCommand(t, "ufw", "allow", "28454", "comment", "manual-other-fixture")
	} else {
		nativeCommand(t, "firewall-cmd", "--add-port=28453/tcp")
		nativeCommand(t, "firewall-cmd", "--add-port=28453/udp")
		nativeCommand(t, "firewall-cmd", "--permanent", "--add-port=28454/tcp")
		nativeCommand(t, "firewall-cmd", "--permanent", "--add-port=28454/udp")
		nativeCommand(t, "firewall-cmd", "--add-port=28454/tcp")
		nativeCommand(t, "firewall-cmd", "--add-port=28454/udp")
	}
	node := &model.NodeSpec{Protocol: "socks", ServerPort: 28453, SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}}
	apply := func() {
		t.Helper()
		node.SourcePolicyReady = true
		node.SourceBlockCIDRs, err = node.SourcePolicy.Blocked(sourcepolicy.Snapshot{})
		if err != nil {
			t.Fatal(err)
		}
		if err = m.Apply(ctx, "node", node, "xray"); err != nil {
			if driver == "nftables" {
				t.Log(nativeCommand(t, "nft", "--json", "list", "table", "inet", b.sourceTable()))
			}
			t.Fatal(err)
		}
	}
	probe := func(ipv4, ipv6 bool) {
		t.Helper()
		for _, protocol := range []string{"tcp", "udp"} {
			probeNative(t, nativeProbe{protocol, "192.0.2.1", 28453, ipv4}, nativeProbe{protocol, "2001:db8:1::1", 28453, ipv6})
		}
	}
	apply()
	probe(false, false)
	probeNative(t, nativeProbe{"tcp", "192.0.2.1", 28454, true}, nativeProbe{"udp", "2001:db8:1::1", 28454, true})
	node.SourcePolicy.AllowIPs = []string{"192.0.2.2", "2001:db8:1::2"}
	apply()
	apply()
	probe(true, true)
	node.SourcePolicy.AllowIPs = []string{"192.0.2.2"}
	apply()
	probe(true, false)
	node.SourcePolicy = &sourcepolicy.Policy{Mode: "blacklist", BlockIPs: []string{"192.0.2.2"}}
	apply()
	probe(false, true)
	node.SourcePolicy.BlockIPs=[]string{"192.0.2.2","2001:db8:1::2"}
	node.ListenIP="192.0.2.1"
	apply()
	probe(false,true)
	node.ListenIP="2001:db8:1::1"
	apply()
	probe(true,false)
	node.ListenIP=""
	node.SourcePolicy.BlockIPs=[]string{"192.0.2.2"}
	apply()
	if driver == "iptables" {
		// 破坏集合内容后须使用交换恢复，不能只检查集合存在。
		prefixes := ipsetSourcePrefixes(sourcePlan{Blocked: node.SourceBlockCIDRs}, 4)
		set := b.sourceSetName(prefixes, 4)
		nativeCommand(t, "ipset", "del", set, "192.0.2.2")
		apply()
		probe(false, true)
	}
	if backend == "ufw" {
		nativeCommand(t, "ufw", "reload")
		// 正常 UFW 重载期间仍须保留独立 mangle/nft 来源过滤，不能等下一次轮询才保护。
		probe(false, true)
	} else {
		nativeCommand(t, "firewall-cmd", "--reload")
	}
	apply()
	probe(false, true)
	if driver == "nftables" {
		nativeCommand(t, "nft", "delete", "table", "inet", b.sourceTable())
	} else {
		for _, name := range []string{"iptables", "ip6tables"} {
			nativeCommand(t, name, "-t", "mangle", "-D", "INPUT", "-m", "comment", "--comment", b.sourceOwner(), "-j", b.sourceChain())
		}
	}
	apply()
	probe(false, true)
	// 异常退出只释放状态锁，重启读取旧归属后精确清理。
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	m.closed = true
	recovered, err := newSystemBackend(cfg, b.scope, nativeCommands{t})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.ApplySources(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Apply(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if recovered.state.SourceDriver != "" || len(recovered.state.SourcePlans) != 0 || len(recovered.state.SourceSets) != 0 || recovered.state.Version != 1 {
		t.Fatal("来源规则清理或状态降级失败")
	}
	probeNative(t, nativeProbe{"tcp", "192.0.2.1", 28454, true}, nativeProbe{"udp", "2001:db8:1::1", 28454, true})
	if backend == "ufw" && !strings.Contains(nativeCommand(t, "ufw", "status"), "manual-other-fixture") {
		t.Fatal("其他端口手工规则被修改")
	}
	t.Logf("%s + %s：双栈 TCP/UDP 白名单、黑名单、重复调用、外部规则、重载恢复、内容损坏恢复与异常退出清理通过", backend, driver)
}
