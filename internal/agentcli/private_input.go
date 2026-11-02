package agentcli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

const maxInstallTokenBytes = 64 * 1024

// 安装凭据经标准输入传递，避免出现在子进程的命令行参数中。
func readInstallToken(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxInstallTokenBytes+1))
	if err != nil {
		return "", fmt.Errorf("read installation credential: %w", err)
	}
	if len(data) > maxInstallTokenBytes {
		return "", errors.New("installation credential exceeds size limit")
	}
	token := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("installation credential must be a single line")
	}
	return token, nil
}
