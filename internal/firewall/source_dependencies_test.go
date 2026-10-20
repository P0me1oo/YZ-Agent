package firewall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type sourceProvisionFixture struct {
	sourceCapabilities
	installed []string
	fail      error
	noTools   bool
}

func (r *sourceProvisionFixture) InstallSourceTools(ctx context.Context, driver string) error {
	r.installed = append(r.installed, driver)
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.fail != nil {
		return r.fail
	}
	if !r.noTools {
		if r.available == nil {
			r.available = map[string]bool{}
		}
		if driver == "nftables" {
			r.available["nft"] = true
		} else {
			for _, name := range sourceIPTablesTools {
				r.available[name] = true
			}
		}
	}
	return nil
}

func TestSourceToolsOnlyProvisionForManagedSourceRestrictions(t *testing.T) {
	for _, test := range []struct {
		name       string
		disabled   bool
		restricted bool
		want       int
	}{
		{"public nodes", false, false, 0},
		{"global management disabled", true, true, 0},
		{"source protection", false, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &sourceProvisionFixture{}
			enabled := !test.disabled
			b := &systemBackend{commands: runner, cfg: config.FirewallConfig{Enabled: &enabled}}
			plans := map[string]Plan{"node": {Restricted: test.restricted}}
			for range 2 {
				if err := b.prepareSourceTools(t.Context(), plans); err != nil {
					t.Fatal(err)
				}
			}
			if len(runner.installed) != test.want {
				t.Fatalf("安装次数=%d，希望=%d", len(runner.installed), test.want)
			}
		})
	}
}

func TestSourceToolsPreferNFTAndPreserveBackendOwnership(t *testing.T) {
	for _, test := range []struct {
		name, configured, saved, want string
		nft, iptables                 bool
	}{
		{name: "auto installs nft", want: "nftables"},
		{name: "UFW with iptables still prepares nft", iptables: true, want: "nftables"},
		{name: "already available nft", nft: true},
		{name: "explicit nft", configured: "nftables", want: "nftables"},
		{name: "explicit iptables", configured: "iptables", want: "iptables", nft: true},
		{name: "saved iptables", saved: "iptables", want: "iptables", nft: true},
		{name: "saved nft beats changed preference", saved: "nftables", configured: "iptables", nft: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &sourceProvisionFixture{sourceCapabilities: sourceCapabilities{available: map[string]bool{"nft": test.nft}}}
			if test.iptables {
				for _, name := range sourceIPTablesTools {
					runner.available[name] = true
				}
				runner.available["ufw"], runner.ufw = true, "Status: active"
			}
			b := &systemBackend{commands: runner, cfg: config.FirewallConfig{SourceBackend: test.configured}, state: savedState{SourceDriver: test.saved}}
			if err := b.prepareSourceTools(t.Context(), map[string]Plan{"node": {Restricted: true}}); err != nil {
				t.Fatal(err)
			}
			var want []string
			if test.want != "" {
				want = []string{test.want}
			}
			if !reflect.DeepEqual(runner.installed, want) {
				t.Fatalf("安装=%v，希望=%v", runner.installed, want)
			}
		})
	}
}

func TestSourceToolsFailureDoesNotBypassProtection(t *testing.T) {
	installErr := errors.New("模拟软件源失败")
	for _, test := range []struct {
		name, configured  string
		fallback, noTools bool
		wantErr           bool
	}{
		{"missing tools", "", false, false, true},
		{"existing iptables fallback", "", true, false, false},
		{"explicit nft cannot switch", "nftables", true, false, true},
		{"installation did not provide nft", "", false, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &sourceProvisionFixture{fail: installErr, noTools: test.noTools}
			runner.available = map[string]bool{}
			if test.noTools {
				runner.fail = nil
			}
			if test.fallback {
				for _, name := range sourceIPTablesTools {
					runner.available[name] = true
				}
			}
			b := &systemBackend{commands: runner, cfg: config.FirewallConfig{SourceBackend: test.configured}}
			err := b.prepareSourceTools(t.Context(), map[string]Plan{"node": {Restricted: true}})
			if (err != nil) != test.wantErr {
				t.Fatalf("错误=%v，希望错误=%v", err, test.wantErr)
			}
			if !test.wantErr {
				driver, err := b.selectSourceDriver(t.Context())
				if err != nil || driver != "iptables" {
					t.Fatalf("未回退到完整的现有后端: %s %v", driver, err)
				}
			}
			if b.state.SourceDriver != "" || len(b.state.SourcePlans) != 0 {
				t.Fatal("工具准备阶段提前更改了规则归属")
			}
		})
	}
	// 环境冲突不能通过安装新工具掩盖。
	runner := &sourceProvisionFixture{sourceCapabilities: sourceCapabilities{available: map[string]bool{"ufw": true, "firewall-cmd": true}, ufw: "Status: active", firewalld: "running"}}
	b := &systemBackend{commands: runner}
	if err := b.prepareSourceTools(t.Context(), map[string]Plan{"node": {Restricted: true}}); err == nil || len(runner.installed) != 0 {
		t.Fatal("冲突环境不应执行安装")
	}
}

func TestSourceDependencyFailurePreventsRuleWrites(t *testing.T) {
	installErr := errors.New("模拟软件安装失败")
	runner := &sourceProvisionFixture{fail: installErr}
	b := &systemBackend{commands: runner, path: filepath.Join(t.TempDir(), "state.json")}
	m := testManager(b)
	node := &model.NodeSpec{Protocol: "socks", ServerPort: 43222,
		SourcePolicy: &sourcepolicy.Policy{Mode: "blacklist"}, SourcePolicyReady: true}
	if err := m.Prepare(t.Context(), "node", node, "singbox"); !errors.Is(err, installErr) || !errors.Is(m.lastErr, installErr) {
		t.Fatalf("安装失败未交给节点生命周期处理: %v", err)
	}
	if _, err := os.Stat(b.path); !os.IsNotExist(err) || b.state.SourceDriver != "" {
		t.Fatal("工具未准备成功就写入了防火墙归属状态")
	}
}

func sourceInstallerFixture(t *testing.T, manager string) (*sourceToolsInstaller, map[string]bool, *[][]string) {
	t.Helper()
	available := map[string]bool{manager: true}
	calls := &[][]string{}
	i := &sourceToolsInstaller{
		gate: make(chan struct{}, 1), available: func(name string) bool { return available[name] },
		check: func() error { return nil }, now: time.Now, failures: map[string]sourceInstallFailure{},
	}
	i.run = func(ctx context.Context, name string, args ...string) error {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > sourceInstallTimeout {
			t.Error("安装命令缺少总时限")
		}
		*calls = append(*calls, append([]string{name}, args...))
		if strings.Contains(strings.Join(args, " "), "nftables") {
			available["nft"] = true
		}
		if strings.Contains(strings.Join(args, " "), "ipset") {
			for _, tool := range sourceIPTablesTools {
				available[tool] = true
			}
		}
		return nil
	}
	return i, available, calls
}

func TestSourcePackageInstallationUsesSupportedManagers(t *testing.T) {
	for _, manager := range []string{"apt-get", "dnf", "yum", "apk"} {
		t.Run(manager, func(t *testing.T) {
			i, _, calls := sourceInstallerFixture(t, manager)
			for range 2 {
				if err := i.install(t.Context(), "nftables"); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if manager == "apt-get" {
				want = 2
				if !strings.Contains(strings.Join((*calls)[0], " "), "update -qq") {
					t.Fatal("APT 安装前没有刷新索引")
				}
			}
			if len(*calls) != want || (*calls)[want-1][len((*calls)[want-1])-1] != "nftables" {
				t.Fatalf("不正确或重复的安装命令: %v", *calls)
			}
		})
	}
	i, available, calls := sourceInstallerFixture(t, "apt-get")
	for _, name := range sourceIPTablesTools[:4] {
		available[name] = true
	}
	if err := i.install(t.Context(), "iptables"); err != nil {
		t.Fatal(err)
	}
	last := (*calls)[len(*calls)-1]
	if last[len(last)-1] != "ipset" || strings.Contains(strings.Join(last, " "), "nftables") {
		t.Fatalf("显式 iptables 应只补装缺失的 ipset: %v", last)
	}
}

func TestSourceInstallerConcurrentCallsAndFailureRetry(t *testing.T) {
	i, _, calls := sourceInstallerFixture(t, "apk")
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := i.install(t.Context(), "nftables"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(*calls) != 1 {
		t.Fatalf("并发节点重复安装软件: %v", *calls)
	}
	i, available, calls := sourceInstallerFixture(t, "apt-get")
	now := time.Now()
	i.now = func() time.Time { return now }
	success := i.run
	installErr := errors.New("模拟索引下载失败")
	i.run = func(ctx context.Context, name string, args ...string) error {
		*calls = append(*calls, append([]string{name}, args...))
		return installErr
	}
	for range 2 {
		if err := i.install(t.Context(), "nftables"); !errors.Is(err, installErr) {
			t.Fatalf("没有保留安装失败原因: %v", err)
		}
	}
	if len(*calls) != 1 || available["nft"] {
		t.Fatal("索引失败后继续安装，或未对重复失败退避")
	}
	now = now.Add(6 * time.Minute)
	i.run = success
	if err := i.install(t.Context(), "nftables"); err != nil || !available["nft"] {
		t.Fatalf("软件源恢复后不能重试: %v", err)
	}
}

func TestSourceInstallerRejectsUnavailableEnvironmentsAndCancellation(t *testing.T) {
	i, _, calls := sourceInstallerFixture(t, "apk")
	i.check = func() error { return errors.New("没有网络管理权限") }
	if err := i.install(t.Context(), "nftables"); err == nil || len(*calls) != 0 {
		t.Fatal("无权限环境执行了软件安装")
	}
	i, available, calls := sourceInstallerFixture(t, "apk")
	delete(available, "apk")
	if err := i.install(t.Context(), "nftables"); err == nil || len(*calls) != 0 {
		t.Fatal("没有包管理器时误报成功")
	}
	i, _, calls = sourceInstallerFixture(t, "apk")
	i.gate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := i.install(ctx, "nftables"); !errors.Is(err, context.Canceled) || len(*calls) != 0 {
		t.Fatalf("等待其他安装时没有响应取消: %v", err)
	}
	<-i.gate
	i.run = func(context.Context, string, ...string) error { return nil }
	if err := i.install(t.Context(), "nftables"); err == nil {
		t.Fatal("安装后未提供 nft 也误报成功")
	}
}
