package logrotate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestRotateIfLargerKeepsLimitedBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yz-agent.log")
	owner, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Init(owner, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("small")
	if rotated, err := RotateIfLarger(path, 10, 2); err != nil || rotated {
		t.Fatalf("未超过上限不应轮转: rotated=%v err=%v", rotated, err)
	}

	for i := 1; i <= 3; i++ {
		write(fmt.Sprintf("round-%d-%s", i, strings.Repeat("x", 20)))
		if rotated, err := RotateIfLarger(path, 10, 2); err != nil || !rotated {
			t.Fatalf("第 %d 次超过上限应轮转: rotated=%v err=%v", i, rotated, err)
		}
		if info, err := os.Stat(path); err != nil || info.Size() != 0 {
			t.Fatalf("轮转后原文件应清空: %v %v", info, err)
		}
	}
	for suffix, want := range map[string]string{".1": "round-3", ".2": "round-2"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil || !strings.HasPrefix(string(data), want) {
			t.Fatalf("%s 应为 %s 的内容，实际 %q err=%v", suffix, want, data, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("超出保留份数的旧日志应删除")
	}
}

// 轮转时切换实际日志写入方，旧内容和新内容都保留。
func TestRotateIfLargerRebindsOwnedWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	nlog.Init(writer, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	if _, err := writer.WriteString(strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if rotated, err := RotateIfLarger(path, 10, 1); err != nil || !rotated {
		t.Fatalf("应轮转: rotated=%v err=%v", rotated, err)
	}
	nlog.Core().Info("after")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "after") {
		t.Fatalf("切换后新文件应收到日志，实际 %q err=%v", data, err)
	}
	old, err := os.ReadFile(path + ".1")
	if err != nil || string(old) != strings.Repeat("a", 64) {
		t.Fatalf("旧日志应完整保留，实际 %q err=%v", old, err)
	}
}

func TestRotateIfLargerIgnoresMissingFile(t *testing.T) {
	if rotated, err := RotateIfLarger(filepath.Join(t.TempDir(), "missing.log"), 10, 3); err != nil || rotated {
		t.Fatalf("文件不存在时不应报错: rotated=%v err=%v", rotated, err)
	}
}

func TestRotatorSwitchesFilesAfterConfigReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := start(ctx, time.Hour, 10, 1)
	t.Cleanup(func() {
		cancel()
		<-r.done
	})
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.log")
	newPath := filepath.Join(dir, "new.log")
	useLogFile := func(path string) {
		t.Helper()
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		nlog.Init(file, slog.LevelInfo, false)
	}
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 20)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitForRotation := func(path string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path + ".1"); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("日志文件 %s 未及时轮转", path)
	}

	// 启动时没有日志文件，热重载后仍应开始轮转。
	write(oldPath)
	useLogFile(oldPath)
	r.Update(oldPath)
	waitForRotation(oldPath)
	write(oldPath)
	write(newPath)
	useLogFile(newPath)
	r.Update(newPath)
	waitForRotation(newPath)
	if info, err := os.Stat(oldPath); err != nil || info.Size() != 20 {
		t.Fatalf("切换后旧日志不应继续轮转: info=%v err=%v", info, err)
	}
	// 停止文件输出后，原来的文件不再属于轮转目标。
	cancel()
	<-r.done
	r.Update()
	write(newPath)
	r.rotate()
	if info, err := os.Stat(newPath); err != nil || info.Size() != 20 {
		t.Fatalf("停止文件输出后不应轮转旧文件: info=%v err=%v", info, err)
	}
}
