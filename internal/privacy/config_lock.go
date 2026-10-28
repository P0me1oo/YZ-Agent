package privacy

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// 配置迁移和管理命令使用独立的锁，不与父安装器持有的安装锁互相等待。
func ConfigLock(ctx context.Context, path string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".private-config-lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if tryConfigLock(f) == nil {
			return func() { _ = unlockConfig(f); _ = f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
