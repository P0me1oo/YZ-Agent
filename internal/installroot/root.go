package installroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Current = "/etc/yz-agent"
const Legacy = "/etc/xboard-node"

// Path 在首次迁移前仍能读取旧安装；目录迁移由安装器持锁执行。
func Path() string {
	if root, err := Resolve(); err == nil {
		return root
	}
	return Current
}

func Resolve() (string, error) { return resolve(Current, Legacy) }

func resolve(current, legacy string) (string, error) {
	currentInfo, currentErr := os.Stat(current)
	legacyInfo, legacyErr := os.Stat(legacy)
	for _, result := range []struct {
		name string
		info os.FileInfo
		err  error
	}{{current, currentInfo, currentErr}, {legacy, legacyInfo, legacyErr}} {
		if result.err != nil && !errors.Is(result.err, os.ErrNotExist) {
			return "", result.err
		}
		if result.err == nil && !result.info.IsDir() {
			return "", fmt.Errorf("installation path is not a directory: %s", result.name)
		}
	}
	if currentErr == nil && legacyErr == nil && !os.SameFile(currentInfo, legacyInfo) {
		return "", errors.New("both installation directories exist; resolve the directory conflict first")
	}
	if errors.Is(currentErr, os.ErrNotExist) && legacyErr == nil {
		return legacy, nil
	}
	return current, nil
}

// CanonicalConfig 保留改名前的防火墙实例标识，搬迁后仍能回收已登记的规则。
func CanonicalConfig(name string) string {
	current := filepath.FromSlash(Current)
	if name == current || strings.HasPrefix(name, current+string(filepath.Separator)) {
		return filepath.FromSlash(Legacy) + strings.TrimPrefix(name, current)
	}
	return name
}
