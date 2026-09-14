package agentcli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func upgradeFixture(t *testing.T) (installPaths, upgradeOperations) {
	t.Helper()
	paths := installPaths{binDir: t.TempDir()}
	for _, name := range []string{"xboard-node", "xbctl"} {
		if err := os.WriteFile(filepath.Join(paths.binDir, name), []byte("old-"+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	checksums := fmt.Sprintf("%x  xboard-node-linux-%s\n%x  xbctl-linux-%s\n",
		sha256.Sum256([]byte("new-xboard-node")), runtime.GOARCH,
		sha256.Sum256([]byte("new-xbctl")), runtime.GOARCH)
	ops := upgradeOperations{
		download: func(url, destination string) error {
			var data []byte
			switch path.Base(url) {
			case "SHA256SUMS":
				data = []byte(checksums)
			case "xboard-node-linux-" + runtime.GOARCH:
				data = []byte("new-xboard-node")
			case "xbctl-linux-" + runtime.GOARCH:
				data = []byte("new-xbctl")
			default:
				return errors.New("unexpected artifact")
			}
			return os.WriteFile(destination, data, 0o600)
		},
		run: func(string, ...string) ([]byte, error) {
			return []byte("xboard-node v1.13-yz.23 (test build)"), nil
		},
		restart: func() error { return nil },
		rename:  os.Rename,
	}
	return paths, ops
}

func TestUpgradeDelegatesNameMigrationBeforeReplacingFiles(t *testing.T) {
	paths, ops := upgradeFixture(t)
	ops.run = func(string, ...string) ([]byte, error) { return []byte("yz-agent v1.14.0 (test build)"), nil }
	called := false
	ops.migrate = func(node, cli string) error {
		called = true
		requireFileContents(t, node, "new-xboard-node")
		if cli != node {
			t.Fatal("统一程序必须复用同一下载文件")
		}
		requireFileContents(t, paths.installedBinary(), "old-xboard-node")
		requireFileContents(t, paths.cli(), "old-xbctl")
		return errors.New("模拟迁移失败")
	}
	if _, err := upgradeBinaries(paths, "v1.14.0", ops); err == nil || !called {
		t.Fatalf("迁移未执行或未返回错误: %v", err)
	}
	requireOriginalUpgradeFiles(t, paths)
}

func TestUpgradePrefersCanonicalArtifactAndDownloadsOnce(t *testing.T) {
	paths, ops := upgradeFixture(t)
	artifact := "yz-agent-linux-" + runtime.GOARCH
	downloads := 0
	ops.download = func(url, destination string) error {
		if path.Base(url) == "SHA256SUMS" {
			return os.WriteFile(destination, []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("new-yz-agent")), artifact)), 0o600)
		}
		if path.Base(url) != artifact {
			t.Fatalf("新版不应下载历史附件: %s", path.Base(url))
		}
		downloads++
		return os.WriteFile(destination, []byte("new-yz-agent"), 0o600)
	}
	ops.run = func(string, ...string) ([]byte, error) { return []byte("yz-agent v1.14.0"), nil }
	ops.migrate = func(node, cli string) error {
		if node != cli {
			t.Fatal("节点和管理命令必须复用同一文件")
		}
		requireFileContents(t, node, "new-yz-agent")
		return nil
	}
	if _, err := upgradeBinaries(paths, "v1.14.0", ops); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 {
		t.Fatalf("程序下载次数 = %d", downloads)
	}
	requireNoUpgradeStage(t, paths)
}

func TestUpgradeAgentInPlaceKeepsAgentFilename(t *testing.T) {
	paths, ops := upgradeFixture(t)
	if err := os.Rename(paths.installedBinary(), paths.binary()); err != nil {
		t.Fatal(err)
	}
	ops.run = func(string, ...string) ([]byte, error) { return []byte("yz-agent v1.14.0 (test build)"), nil }
	ops.migrate = func(node, cli string) error {
		if node != cli {
			t.Fatal("统一程序不应下载独立管理器")
		}
		return os.Rename(node, paths.binary())
	}
	for i := 0; i < 2; i++ {
		if _, err := upgradeBinaries(paths, "v1.14.0", ops); err != nil {
			t.Fatal(err)
		}
		requireFileContents(t, paths.binary(), "new-xboard-node")
		if _, err := os.Stat(filepath.Join(paths.binDir, "xboard-node")); !os.IsNotExist(err) {
			t.Fatalf("升级不应恢复旧文件名: %v", err)
		}
		requireNoUpgradeStage(t, paths)
	}
}

func TestUpgradeAgentDowngradeUsesMigration(t *testing.T) {
	paths, ops := upgradeFixture(t)
	if err := os.Rename(paths.installedBinary(), paths.binary()); err != nil {
		t.Fatal(err)
	}
	called := false
	ops.migrate = func(node, cli string) error { called = true; return nil }
	if _, err := upgradeBinaries(paths, "v1.13-yz.23", ops); err != nil || !called {
		t.Fatalf("历史版本回退必须迁移程序和服务名称: %v", err)
	}
}

func requireFileContents(t *testing.T, file, expected string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != expected {
		t.Fatalf("%s 内容 = %q, 错误 = %v; 预期 %q", file, data, err, expected)
	}
}

func requireOriginalUpgradeFiles(t *testing.T, paths installPaths) {
	t.Helper()
	requireFileContents(t, paths.installedBinary(), "old-xboard-node")
	requireFileContents(t, paths.cli(), "old-xbctl")
	requireNoUpgradeStage(t, paths)
}

func requireNoUpgradeStage(t *testing.T, paths installPaths) {
	t.Helper()
	entries, err := os.ReadDir(paths.binDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".yz-agent-upgrade-") {
			t.Fatalf("升级留下临时目录: %s", entry.Name())
		}
	}
}

func TestUpgradeUsesTwoCopiesAndSupportsRepeatedCalls(t *testing.T) {
	t.Parallel()
	paths, ops := upgradeFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		linksChecked := 0
		ops.rename = func(source, destination string) error {
			name := filepath.Base(source)
			if name == "xboard-node" || name == "xbctl" {
				// 在替换前比较身份，避免 Windows 延迟读取文件编号时读到新版文件。
				original, err := os.Stat(destination)
				if err != nil {
					t.Fatal(err)
				}
				backup, err := os.Stat(filepath.Join(filepath.Dir(source), "previous-"+name))
				if err != nil || !os.SameFile(original, backup) {
					t.Fatalf("%s 的备份必须复用原文件，不能复制内容: %v", name, err)
				}
				linksChecked++
			}
			return os.Rename(source, destination)
		}
		ops.restart = func() error {
			stages, err := filepath.Glob(filepath.Join(paths.binDir, ".yz-agent-upgrade-*"))
			if err != nil || len(stages) != 1 {
				t.Fatalf("升级临时目录 = %v, 错误 = %v", stages, err)
			}
			if linksChecked != 2 {
				t.Fatalf("未确认两份备份的文件身份: %d", linksChecked)
			}
			for _, name := range []string{"xboard-node", "xbctl"} {
				prefix := "old-"
				if attempt > 0 {
					prefix = "new-"
				}
				requireFileContents(t, filepath.Join(stages[0], "previous-"+name), prefix+name)
				requireFileContents(t, filepath.Join(paths.binDir, name), "new-"+name)
			}
			return nil
		}
		version, err := upgradeBinaries(paths, "latest", ops)
		if err != nil || version != "v1.13-yz.23" {
			t.Fatalf("升级结果 = %q, %v", version, err)
		}
		requireNoUpgradeStage(t, paths)
	}
}

func TestUpgradeCleansInterruptedDownloads(t *testing.T) {
	t.Parallel()
	for _, artifact := range []string{"SHA256SUMS", "xboard-node-linux-" + runtime.GOARCH, "xbctl-linux-" + runtime.GOARCH} {
		t.Run(artifact, func(t *testing.T) {
			paths, ops := upgradeFixture(t)
			legacy := filepath.Join(paths.binDir, ".xboard-node.new")
			if err := os.WriteFile(legacy, []byte("existing-user-file"), 0o600); err != nil {
				t.Fatal(err)
			}
			download := ops.download
			ops.download = func(url, destination string) error {
				if path.Base(url) == artifact {
					if err := os.WriteFile(destination, []byte("partial"), 0o600); err != nil {
						return err
					}
					return syscall.ENOSPC
				}
				return download(url, destination)
			}
			ops.restart = func() error { t.Fatal("下载失败不能重启服务"); return nil }
			if _, err := upgradeBinaries(paths, "latest", ops); !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("升级错误 = %v", err)
			}
			requireOriginalUpgradeFiles(t, paths)
			requireFileContents(t, legacy, "existing-user-file")
		})
	}
}

func TestUpgradeRejectsInvalidReleaseBeforeReplacement(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"checksum", "version", "custom-directory-support"} {
		t.Run(failure, func(t *testing.T) {
			paths, ops := upgradeFixture(t)
			download := ops.download
			ops.download = func(url, destination string) error {
				if failure == "checksum" && strings.HasPrefix(path.Base(url), "xbctl-linux-") {
					return os.WriteFile(destination, []byte("corrupt"), 0o600)
				}
				return download(url, destination)
			}
			ops.run = func(file string, args ...string) ([]byte, error) {
				if failure == "version" || failure == "custom-directory-support" && args[0] == "config" {
					return nil, errors.New("unsupported executable")
				}
				return []byte("xboard-node v1.13-yz.23"), nil
			}
			ops.restart = func() error { t.Fatal("校验失败不能重启服务"); return nil }
			if _, err := upgradeBinaries(paths, "latest", ops); err == nil {
				t.Fatal("无效发布应被拒绝")
			}
			requireOriginalUpgradeFiles(t, paths)
		})
	}
}

func TestUpgradeRestoresFilesAfterReplacementError(t *testing.T) {
	t.Parallel()
	for _, failedFile := range []string{"xboard-node", "xbctl"} {
		t.Run(failedFile, func(t *testing.T) {
			paths, ops := upgradeFixture(t)
			ops.rename = func(source, destination string) error {
				if filepath.Base(source) == failedFile {
					return errors.New("replace failed")
				}
				return os.Rename(source, destination)
			}
			ops.restart = func() error { t.Fatal("替换失败不能重启服务"); return nil }
			if _, err := upgradeBinaries(paths, "latest", ops); err == nil {
				t.Fatal("应报告替换失败")
			}
			requireOriginalUpgradeFiles(t, paths)
		})
	}
}

func TestUpgradeRestartsOriginalFilesOnFailure(t *testing.T) {
	t.Parallel()
	paths, ops := upgradeFixture(t)
	restarts := 0
	ops.restart = func() error {
		restarts++
		if restarts == 1 {
			return errors.New("new service failed")
		}
		requireFileContents(t, paths.installedBinary(), "old-xboard-node")
		requireFileContents(t, paths.cli(), "old-xbctl")
		return nil
	}
	if _, err := upgradeBinaries(paths, "latest", ops); err == nil || !strings.Contains(err.Error(), "original binaries restored") {
		t.Fatalf("升级错误 = %v", err)
	}
	if restarts != 2 {
		t.Fatalf("重启次数 = %d", restarts)
	}
	requireOriginalUpgradeFiles(t, paths)
}

func TestUpgradePreservesBackupWhenRollbackFails(t *testing.T) {
	t.Parallel()
	paths, ops := upgradeFixture(t)
	ops.restart = func() error { return errors.New("restart failed") }
	ops.rename = func(source, destination string) error {
		if filepath.Base(source) == "previous-xboard-node" {
			return errors.New("restore failed")
		}
		return os.Rename(source, destination)
	}
	_, err := upgradeBinaries(paths, "latest", ops)
	if err == nil || !strings.Contains(err.Error(), "recovery files retained") {
		t.Fatalf("应指出恢复文件位置: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(paths.binDir, ".yz-agent-upgrade-*"))
	if err != nil || len(stages) != 1 {
		t.Fatalf("恢复目录 = %v, 错误 = %v", stages, err)
	}
	requireFileContents(t, filepath.Join(stages[0], "previous-xboard-node"), "old-xboard-node")
	requireFileContents(t, paths.cli(), "old-xbctl")
}
