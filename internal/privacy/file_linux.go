//go:build linux

package privacy

import (
	"os"

	"golang.org/x/sys/unix"
)

func openForRead(path string) (*os.File, error) {
	// 路径在检查后可能被替换为管道，打开也必须有界。
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}

func openRootForRead(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
