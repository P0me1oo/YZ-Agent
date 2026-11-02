package agentcli

import "github.com/P0me1oo/YZ-Agent/internal/privacy"

// 敏感配置经统一的权限、原子写入和加密出口保存。
func writePrivateFile(path string, data []byte) error {
	return privacy.WriteFile(path, data, 0o600)
}
