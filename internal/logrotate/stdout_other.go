//go:build !linux

package logrotate

import "os"

// StdoutFiles 只在 Linux 上识别重定向到文件的输出；其他平台返回空。
func StdoutFiles() []string { return nil }

func stdoutFDs(string) []int           { return nil }
func rebindStdout(int, *os.File) error { return nil }
