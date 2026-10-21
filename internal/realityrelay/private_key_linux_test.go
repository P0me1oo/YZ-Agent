package realityrelay

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPrivateKeyRejectsFIFOWithoutBlocking(t *testing.T) {
	cfg, _, _ := testConfig(t)
	path := filepath.Join(t.TempDir(), "private.key")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Keys[0].PrivateKeyEnv = ""
	cfg.Keys[0].PrivateKeyFile = path
	done := make(chan error, 1)
	go func() {
		_, err := New(cfg)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "普通文件") {
			t.Fatal("命名管道没有被当作非普通文件拒绝")
		}
	case <-time.After(time.Second):
		// 旧实现会等待写入端；先解除阻塞，避免回归失败时遗留测试协程。
		fd, err := unix.Open(path, unix.O_WRONLY|unix.O_NONBLOCK, 0)
		if err == nil {
			unix.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("私钥文件类型检查被命名管道阻塞")
	}
}
