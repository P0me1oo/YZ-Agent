package xray

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	xrayCore "github.com/xtls/xray-core/core"
)

func TestXrayRunningChecksActualInstance(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	x.instance = new(xrayCore.Instance)
	x.running.Store(true)
	if x.IsRunning() {
		t.Fatal("缓存标记不能掩盖已停止的实例")
	}
	if err := x.instance.Start(); err != nil {
		t.Fatal(err)
	}
	if !x.IsRunning() {
		t.Fatal("实际实例运行时应报告运行")
	}
	if err := x.instance.Close(); err != nil {
		t.Fatal(err)
	}
	if x.IsRunning() {
		t.Fatal("实例关闭后应报告停止")
	}
	x.instance = nil
	if x.IsRunning() {
		t.Fatal("实例不存在时应报告停止")
	}
}
