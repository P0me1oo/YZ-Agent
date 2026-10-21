//go:build unix

package realityrelay

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPrivateKeyFile(path string) (*os.File, error) {
	// 非阻塞打开后再核验实际文件类型，命名管道及替换竞态都不会等待写入端。
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
