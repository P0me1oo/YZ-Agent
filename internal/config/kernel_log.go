package config

import (
	"fmt"
	"strings"
)

// ResolveKernelLogLevel 面板覆盖为空时恢复本地等级；返回两种核心通用的等级名。
func ResolveKernelLogLevel(local, remote string) (string, error) {
	level := strings.ToLower(strings.TrimSpace(remote))
	if level == "" {
		level = strings.ToLower(strings.TrimSpace(local))
	}
	if level == "" {
		level = "warn"
	}
	switch level {
	case "warning":
		return "warn", nil
	case "none":
		return "disabled", nil
	case "debug", "info", "warn", "error", "fatal", "panic", "trace", "disabled":
		return level, nil
	default:
		return "", fmt.Errorf("invalid kernel log level %q", level)
	}
}
