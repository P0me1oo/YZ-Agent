package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherSerializesCallbacksAndKeepsLatestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	write := func(value int) {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("panel:\n  url: https://panel.test\n  token: test\n  node_id: 1\nnode:\n  pull_interval: %d\n", value)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(1)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var calls, active atomic.Int32
	latest := make(chan int, 2)
	w, err := WatchConfig(t.Context(), path, func(c *Config) {
		if active.Add(1) != 1 {
			t.Error("重载回调重叠")
		}
		defer active.Add(-1)
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-release
		}
		latest <- c.Node.PullInterval
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	write(10)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("未监听到配置变化")
	}
	write(20)
	write(30)
	close(release)
	<-latest
	select {
	case value := <-latest:
		if value != 30 {
			t.Fatalf("最终配置=%d", value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("重载期间漏掉最终文件")
	}
}
