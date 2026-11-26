//go:build linux

package firewall

import (
	"errors"
	"runtime"

	"golang.org/x/sys/unix"
)

func (systemCommands) IPv6Available() (bool, error) {
	// 在执行规则的同一线程网络空间探测，不连接网络或绑定端口。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, unix.Close(fd)
}
