//go:build linux

package systemwg

import (
	"context"
	"fmt"
	"runtime"

	"github.com/vishvananda/netns"
)

// InNamespace 只在创建套接字时进入目标空间，连接传输不占用专用线程。
// 恢复失败时让 Go 销毁锁定线程，避免其他线路继承错误的网络空间。
func InNamespace(ctx context.Context, path string, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		original, err := netns.Get()
		if err != nil {
			runtime.UnlockOSThread()
			result <- err
			return
		}
		defer original.Close()
		target, err := netns.GetFromPath(path)
		if err != nil {
			runtime.UnlockOSThread()
			result <- err
			return
		}
		defer target.Close()
		if err = netns.Set(target); err != nil {
			runtime.UnlockOSThread()
			result <- err
			return
		}
		err = ctx.Err()
		if err == nil {
			err = action()
		}
		if restoreErr := netns.Set(original); restoreErr != nil {
			result <- fmt.Errorf("恢复原网络空间失败: %w", restoreErr)
			return
		}
		runtime.UnlockOSThread()
		result <- err
	}()
	return <-result
}
