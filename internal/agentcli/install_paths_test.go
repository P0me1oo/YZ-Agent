package agentcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadInstallPaths(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "bin-dir")
	paths, err := loadInstallPaths(file)
	if err != nil || paths.binDir != defaultBinDir {
		t.Fatalf("旧安装目录解析失败: %+v, %v", paths, err)
	}
	if err := os.WriteFile(file, []byte("/boot/node-bin/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err = loadInstallPaths(file)
	if err != nil || paths.binary() != "/boot/node-bin/yz-agent" || paths.cli() != "/boot/node-bin/yz-agent" {
		t.Fatalf("自定义目录解析失败: %+v, %v", paths, err)
	}
}

func TestLoadInstallPathsRejectsInvalidRecord(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "relative/path\n", "/\n", "/boot/../usr\n", "/boot/./node\n", "//boot/node\n", "/boot//node\n", "/boot/node bin\n", "/boot/$node\n", "/boot/node\n\n", "/boot/node\r\n"} {
		t.Run(content, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "bin-dir")
			if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadInstallPaths(file); err == nil {
				t.Fatal("损坏的目录记录不能退回默认目录")
			}
		})
	}
}

func TestInstallationLockRejectsConcurrentMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	release, err := lockInstallation(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockInstallation(root); err == nil {
		other()
		t.Fatal("同一安装目录不应允许两个写入过程")
	}
	release()
	second, err := lockInstallation(root)
	if err != nil {
		t.Fatal(err)
	}
	second()
}

func TestInstallationLockFollowsDirectoryMigration(t *testing.T) {
	dir := t.TempDir()
	previous, current := filepath.Join(dir, "previous"), filepath.Join(dir, "current")
	release, err := lockInstallationRoots([]string{previous, current})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.Rename(previous, current); err != nil {
		t.Fatal(err)
	}
	if other, err := lockInstallation(current); err == nil {
		other()
		t.Fatal("搬迁后丢失安装锁")
	}
	release()
	if _, err := os.Stat(filepath.Join(current, installLockName)); !os.IsNotExist(err) {
		t.Fatalf("父进程没有释放搬迁后的锁: %v", err)
	}
}

func TestCustomDirectoryServiceDefinitions(t *testing.T) {
	t.Parallel()
	paths := installPaths{binDir: "/boot/node-bin"}
	for _, manager := range []serviceManager{serviceManagerSystemd, serviceManagerOpenRC} {
		_, data, _, err := serviceDefinitionForPaths(manager, paths)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, paths.binary()) || strings.Contains(text, "/usr/local/bin/xboard-node") {
			t.Fatalf("服务未使用自定义程序: %s", text)
		}
		if manager == serviceManagerSystemd && !strings.Contains(text, "RequiresMountsFor=/boot/node-bin") {
			t.Fatal("systemd 必须等待程序目录挂载")
		}
		if manager == serviceManagerOpenRC && !strings.Contains(text, "need net localmount") {
			t.Fatal("OpenRC 必须等待本地分区挂载")
		}
	}
}
