//go:build linux

package logrotate

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

// StdoutFiles 返回标准输出和标准错误所指向的普通文件路径，例如 OpenRC 的日志文件。
// 输出到终端、管道（journald、Docker）时返回空。
func StdoutFiles() []string {
	var paths []string
	for fd, file := range map[int]*os.File{1: os.Stdout, 2: os.Stderr} {
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		path, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil || strings.HasSuffix(path, " (deleted)") {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func stdoutFDs(path string) []int {
	want, err := os.Stat(path)
	if err != nil {
		return nil
	}
	var fds []int
	for fd, file := range map[int]*os.File{1: os.Stdout, 2: os.Stderr} {
		info, err := file.Stat()
		if err == nil && info.Mode().IsRegular() && os.SameFile(want, info) {
			fds = append(fds, fd)
		}
	}
	return fds
}

func rebindStdout(fd int, next *os.File) error { return unix.Dup2(int(next.Fd()), fd) }
