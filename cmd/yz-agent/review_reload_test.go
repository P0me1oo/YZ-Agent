package main

import (
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestReloadReadsFinalFileAfterServicesDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	write := func(interval int) {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("panel:\n  url: https://panel.example.test\n  token: test\n  node_id: 1\nnode:\n  pull_interval: %d\n", interval)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(10)
	a, err := config.LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	write(20)
	b, err := config.LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	// A 正在排空时，B 被通知，文件又写成 C。
	write(30)
	latest := readFinalReloadConfig(path, b)
	if latest.Config.Node.PullInterval != 30 || latest == a || latest == b {
		t.Fatal("重载采用了提前读取的旧配置")
	}
	write(-1)
	if readFinalReloadConfig(path, b) != b {
		t.Fatal("无效最终状态没有保留最后有效配置")
	}
}
