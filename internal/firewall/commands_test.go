package firewall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func firewallHelperArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestSystemCommandsHelper$", "--", mode}, args...)
}

// 使用专用子进程模拟外部命令，不调用或修改系统防火墙。
func TestSystemCommandsHelper(t *testing.T) {
	if os.Getenv("YZ_FIREWALL_COMMAND_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "echo":
		_, _ = io.Copy(os.Stdout, os.Stdin)
		fmt.Fprint(os.Stderr, "stderr-fixture")
	case "fail":
		fmt.Fprint(os.Stderr, "rule-comment-fixture")
		os.Exit(7)
	case "hold":
		time.Sleep(4 * time.Second)
	case "fork", "fork-exit":
		child := exec.Command(os.Args[0], firewallHelperArgs("hold")...)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			_ = child.Process.Kill()
			os.Exit(2)
		}
		if args[0] == "fork" {
			time.Sleep(10 * time.Second)
		}
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func TestSystemCommandsInputOutputAndFailure(t *testing.T) {
	t.Setenv("YZ_FIREWALL_COMMAND_HELPER", "1")
	for range 2 {
		output, err := (systemCommands{}).Run(t.Context(), os.Args[0], "input-fixture\n", firewallHelperArgs("echo")...)
		if err != nil || !strings.Contains(output, "input-fixture\n") || !strings.Contains(output, "stderr-fixture") {
			t.Fatalf("命令输入输出不完整：output=%q error=%v", output, err)
		}
	}
	output, err := (systemCommands{}).Run(t.Context(), os.Args[0], "", firewallHelperArgs("fail")...)
	if err == nil || !strings.Contains(output, "rule-comment-fixture") || strings.Contains(err.Error(), "rule-comment-fixture") {
		t.Fatalf("应保留命令失败且不把规则内容写入错误：output=%q error=%v", output, err)
	}
}

func TestSystemCommandsDeadline(t *testing.T) {
	t.Setenv("YZ_FIREWALL_COMMAND_HELPER", "1")
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := (systemCommands{}).Run(ctx, os.Args[0], "", firewallHelperArgs("hold")...)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("命令未按父上下文超时退出：elapsed=%s error=%v", time.Since(started), err)
	}
}

func firewallHelperPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil || pid <= 0 {
		t.Fatalf("无效的测试子进程 PID：%q", data)
	}
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	return pid
}

func assertFirewallHelperStopped(t *testing.T, pid int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	deadline := time.Now().Add(time.Second)
	for {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		// 已终止但尚未被系统回收的子进程不会再执行防火墙操作。
		_, state, ok := strings.Cut(string(data), ") ")
		if err == nil && ok && strings.HasPrefix(state, "Z ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("命令返回后测试子进程 %d 仍在运行", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSystemCommandsCancellationClosesInheritedOutput(t *testing.T) {
	t.Setenv("YZ_FIREWALL_COMMAND_HELPER", "1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	result := make(chan error, 1)
	go func() {
		_, err := (systemCommands{}).Run(ctx, os.Args[0], "", firewallHelperArgs("fork", pidPath)...)
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(pidPath); err == nil && len(data) > 0 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("测试命令未创建子进程：%v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("等待测试子进程超时")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid := firewallHelperPID(t, pidPath)
	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应保留上下文取消原因：%v", err)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("取消命令后仍在等待子进程关闭输出")
	}
	t.Logf("取消后返回耗时：%s", time.Since(started))
	assertFirewallHelperStopped(t, pid)
}

func TestSystemCommandsParentExitDoesNotLeaveDescendants(t *testing.T) {
	t.Setenv("YZ_FIREWALL_COMMAND_HELPER", "1")
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	started := time.Now()
	_, err := (systemCommands{}).Run(t.Context(), os.Args[0], "", firewallHelperArgs("fork-exit", pidPath)...)
	pid := firewallHelperPID(t, pidPath)
	if !errors.Is(err, exec.ErrWaitDelay) || time.Since(started) > 4*time.Second {
		t.Fatalf("主进程退出后仍被继承的输出管道阻塞：elapsed=%s error=%v", time.Since(started), err)
	}
	assertFirewallHelperStopped(t, pid)
}
