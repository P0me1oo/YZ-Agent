package agentcli

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
)

// 内核锁随执行器退出自动释放，不会因服务重启遗留永久忙碌状态。
func lockRemoteExecution() (func(), error) {
	f, err := os.OpenFile(filepath.Join(defaultInstallRoot, "remote-operation.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
