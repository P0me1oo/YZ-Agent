package firewall

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// UFW 在 IPv6 禁用时可能不显示持久规则；回收归属前另外核对文件中的注释。
func (systemCommands) UFWIPv6Comments() ([]string, error) {
	data, err := os.ReadFile("/etc/ufw/user6.rules")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ufwPersistentComments(string(data))
}

func ufwPersistentComments(data string) ([]string, error) {
	comments := []string{}
	for _, line := range strings.Split(data, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "### tuple ###") {
			continue
		}
		_, encoded, ok := strings.Cut(line, " comment=")
		if !ok {
			continue
		}
		decoded, err := hex.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return nil, fmt.Errorf("无法核对 UFW 持久规则注释编码")
		}
		comments = append(comments, string(decoded))
	}
	return comments, nil
}
