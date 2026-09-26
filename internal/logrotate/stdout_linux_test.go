//go:build linux

package logrotate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 子进程把标准输出和标准错误指向同一文件，验证轮转后两个描述符都切到新文件。
func TestRotateRebindsOpenRCOutputFiles(t *testing.T) {
	if os.Getenv("YZ_LOG_ROTATE_CHILD") == "1" {
		path := os.Getenv("YZ_LOG_ROTATE_PATH")
		fmt.Fprint(os.Stdout, strings.Repeat("a", 64))
		fmt.Fprint(os.Stderr, "before-stderr")
		if rotated, err := RotateIfLarger(path, 10, 1); err != nil || !rotated {
			t.Fatalf("轮转失败: rotated=%v err=%v", rotated, err)
		}
		fmt.Fprint(os.Stdout, "after-stdout")
		fmt.Fprint(os.Stderr, "after-stderr")
		return
	}
	path := filepath.Join(t.TempDir(), "openrc.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRotateRebindsOpenRCOutputFiles$")
	cmd.Env = append(os.Environ(), "YZ_LOG_ROTATE_CHILD=1", "YZ_LOG_ROTATE_PATH="+path)
	cmd.Stdout, cmd.Stderr = file, file
	err = cmd.Run()
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path + ".1")
	if err != nil || !strings.Contains(string(old), "before-stderr") || strings.Contains(string(old), "after-") {
		t.Fatalf("旧文件内容错误: %q err=%v", old, err)
	}
	next, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(next), "after-stdout") || !strings.Contains(string(next), "after-stderr") {
		t.Fatalf("新文件未收到两路输出: %q err=%v", next, err)
	}
}
