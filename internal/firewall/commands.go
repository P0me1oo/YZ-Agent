package firewall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type commands interface {
	Available(string) bool
	Run(context.Context, string, string, ...string) (string, error)
}

type systemCommands struct{}

func (systemCommands) Available(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, fmt.Errorf("防火墙命令输出超过限制")
	}
	return b.Buffer.Write(p)
}

func (systemCommands) Run(parent context.Context, name, input string, args ...string) (string, error) {
	return runSystemCommand(parent, 5*time.Second, name, input, args, nil)
}

func runSystemCommand(parent context.Context, timeout time.Duration, name, input string, args, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// 子进程可能继续持有输出管道，单独终止主进程不能保证 Run 按时返回。
	cmd.WaitDelay = time.Second
	configureCommandCancellation(cmd)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.Env = append(cmd.Env, env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var output limitedOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err != nil && cmd.Process != nil {
		// 主进程已退出但管道仍被占用时，也清理本命令留下的子进程。
		_ = cmd.Cancel()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		return output.String(), commandFailure(name, output.String(), err)
	}
	return output.String(), nil
}

func commandFailure(name, output string, err error) error {
	// 仅提取已知固定诊断，不复制规则内容、地址或管理员注释。
	if name == "ufw" && strings.Contains(output, "ERROR: IPv6 support not enabled") {
		return fmt.Errorf("ufw IPv6 支持未启用: %w", err)
	}
	return fmt.Errorf("%s 执行失败: %w", name, err)
}
