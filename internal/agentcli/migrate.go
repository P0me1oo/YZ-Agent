package agentcli

import (
	"os"
	"os/exec"
	"path/filepath"

	installer "github.com/P0me1oo/YZ-Agent"
)

// 调用同一版本内嵌的安装器，复用名称迁移、健康检查与事务回滚。
// 父进程持有安装锁，子进程只借用，不释放这把锁。
func migrateInstallation(paths installPaths, release, node, cli string) error {
	dir, err := os.MkdirTemp("", "yz-agent-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "install.sh")
	if err := os.WriteFile(script, []byte(installer.Script), 0o600); err != nil {
		return err
	}
	cmd := exec.Command("bash", script, "upgrade", "--version", release,
		"--binary", node, "--xbctl-binary", cli, "--bin-dir", paths.binDir)
	cmd.Env = append(os.Environ(), "YZ_INSTALL_PARENT_LOCK=1")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}
