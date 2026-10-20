package firewall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

var errSourceToolsMissing = errors.New("来源限制需要 nft，或 iptables、ip6tables、对应 restore 命令与 ipset；缺少可用的防火墙工具")

const sourceInstallTimeout = 120 * time.Second

var sourceIPTablesTools = []string{"iptables", "ip6tables", "iptables-restore", "ip6tables-restore", "ipset"}

type sourceToolProvisioner interface {
	InstallSourceTools(context.Context, string) error
}

func sourceToolsAvailable(available func(string) bool, driver string) bool {
	if driver == "nftables" {
		return available("nft")
	}
	if driver != "iptables" {
		return false
	}
	for _, name := range sourceIPTablesTools {
		if !available(name) {
			return false
		}
	}
	return true
}

// 只为确实托管来源限制的节点补装工具；普通端口放行和手工模式不触发安装。
func (b *systemBackend) prepareSourceTools(ctx context.Context, plans map[string]Plan) error {
	if !b.cfg.IsEnabled() {
		return nil
	}
	required := false
	for _, plan := range plans {
		required = required || plan.Restricted
	}
	if !required {
		return nil
	}
	driver := b.state.SourceDriver
	if driver == "" {
		driver = b.cfg.SourceBackend
	}
	automatic := driver == "" || driver == "auto"
	if automatic {
		if err := b.checkSourceEnvironment(ctx); err != nil {
			return err
		}
		driver = "nftables"
	}
	if sourceToolsAvailable(b.commands.Available, driver) {
		return nil
	}
	var err error = errSourceToolsMissing
	if installer, ok := b.commands.(sourceToolProvisioner); ok {
		err = installer.InstallSourceTools(ctx, driver)
		if err == nil && !sourceToolsAvailable(b.commands.Available, driver) {
			err = fmt.Errorf("安装 %s 后仍未找到完整的来源过滤工具", driver)
		}
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 仅新建自动后端允许回退，已有规则及显式后端不在运行中切换。
	if automatic && b.sourceIPTablesAvailable() {
		nlog.Core().Warn("nftables 不可用，沿用已有的 iptables/ipset 来源过滤", "error", err)
		return nil
	}
	return fmt.Errorf("准备来源过滤工具（%s）: %w", driver, err)
}

type sourceInstallFailure struct {
	err   error
	retry time.Time
}

type sourceToolsInstaller struct {
	gate      chan struct{}
	available func(string) bool
	check     func() error
	run       func(context.Context, string, ...string) error
	now       func() time.Time
	failures  map[string]sourceInstallFailure
}

var defaultSourceToolsInstaller = &sourceToolsInstaller{
	gate:      make(chan struct{}, 1),
	available: (systemCommands{}).Available,
	check:     checkSourceInstallPrivileges,
	run:       runSourceInstallCommand,
	now:       time.Now,
	failures:  map[string]sourceInstallFailure{},
}

func (systemCommands) InstallSourceTools(ctx context.Context, driver string) error {
	return defaultSourceToolsInstaller.install(ctx, driver)
}

func checkSourceInstallPrivileges() error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return fmt.Errorf("自动安装来源过滤工具需要 Linux root 权限")
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return fmt.Errorf("无法确认网络管理权限: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(value), 16, 64)
			if err == nil && caps&(1<<12) != 0 {
				return nil
			}
			break
		}
	}
	return fmt.Errorf("缺少 CAP_NET_ADMIN，无法管理来源过滤规则；未自动安装工具")
}

func runSourceInstallCommand(ctx context.Context, name string, args ...string) error {
	// 不触发 needrestart 的自动服务重启，也不主动加载发行版的默认防火墙规则。
	_, err := runSystemCommand(ctx, sourceInstallTimeout, name, "", args, []string{
		"DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l", "APT_LISTCHANGES_FRONTEND=none", "SYSTEMD_OFFLINE=1",
	})
	return err
}

func (i *sourceToolsInstaller) install(parent context.Context, driver string) error {
	ctx, cancel := context.WithTimeout(parent, sourceInstallTimeout)
	defer cancel()
	select {
	case i.gate <- struct{}{}:
		defer func() { <-i.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if driver != "nftables" && driver != "iptables" {
		return fmt.Errorf("不支持自动安装来源过滤后端 %q", driver)
	}
	if sourceToolsAvailable(i.available, driver) {
		return nil
	}
	if failure, ok := i.failures[driver]; ok && i.now().Before(failure.retry) {
		return fmt.Errorf("来源过滤工具安装失败，稍后重试: %w", failure.err)
	}
	if err := i.check(); err != nil {
		return err
	}
	manager := ""
	for _, name := range []string{"apt-get", "dnf", "yum", "apk"} {
		if i.available(name) {
			manager = name
			break
		}
	}
	if manager == "" {
		return fmt.Errorf("未找到支持的包管理器，请手动安装 %s 所需工具", driver)
	}
	packages := []string{"nftables"}
	if driver == "iptables" {
		packages = nil
		for _, name := range sourceIPTablesTools[:4] {
			if !i.available(name) {
				packages = append(packages, "iptables")
				break
			}
		}
		if manager == "apk" && (!i.available("ip6tables") || !i.available("ip6tables-restore")) {
			packages = append(packages, "ip6tables")
		}
		if !i.available("ipset") {
			packages = append(packages, "ipset")
		}
	}
	nlog.Core().Info("正在补装来源过滤工具", "manager", manager, "packages", strings.Join(packages, ","))
	err := i.installPackages(ctx, manager, packages)
	if err == nil && !sourceToolsAvailable(i.available, driver) {
		err = fmt.Errorf("软件包安装完成，但 %s 来源过滤工具仍不完整", driver)
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		i.failures[driver] = sourceInstallFailure{err: err, retry: i.now().Add(5 * time.Minute)}
		return fmt.Errorf("自动安装来源过滤工具失败: %w", err)
	}
	delete(i.failures, driver)
	nlog.Core().Info("来源过滤工具已就绪", "backend", driver)
	return nil
}

func (i *sourceToolsInstaller) installPackages(ctx context.Context, manager string, packages []string) error {
	switch manager {
	case "apt-get":
		options := []string{"-o", "DPkg::Lock::Timeout=20", "-o", "Acquire::Retries=1", "-o", "Acquire::http::Timeout=15", "-o", "Acquire::https::Timeout=15"}
		if err := i.run(ctx, manager, append(append([]string{}, options...), "update", "-qq", "-o", "APT::Update::Error-Mode=any")...); err != nil {
			return err
		}
		args := append(append([]string{}, options...), "install", "-y", "--no-install-recommends", "-o", "Dpkg::Options::=--force-confold")
		return i.run(ctx, manager, append(args, packages...)...)
	case "dnf", "yum":
		return i.run(ctx, manager, append([]string{"install", "-y", "--setopt=install_weak_deps=False"}, packages...)...)
	case "apk":
		return i.run(ctx, manager, append([]string{"add", "--no-cache"}, packages...)...)
	default:
		return fmt.Errorf("不支持的包管理器 %q", manager)
	}
}
