//go:build linux

package firewall

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureCommandCancellation(cmd *exec.Cmd) {
	// UFW 会启动 iptables 等子进程，使用独立进程组避免遗留命令继续修改规则。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
