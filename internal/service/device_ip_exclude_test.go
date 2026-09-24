package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// 只改名单时配置哈希不变，内核不重载，但名单立即生效；缺少字段等同清空。
func TestDeviceIPExcludeAppliesWithoutKernelReload(t *testing.T) {
	t.Cleanup(func() { deviceip.SetExcluded(nil) })

	decode := func(raw string) *model.NodeSpec {
		var nc panel.NodeConfig
		if err := json.Unmarshal([]byte(raw), &nc); err != nil {
			t.Fatal(err)
		}
		return model.NodeSpecFromPanel(&nc)
	}
	base := decode(`{"protocol":"vless","server_port":443}`)
	withList := decode(`{"protocol":"vless","server_port":443,"device_ip_exclude":["203.0.114.0/24"]}`)
	if computeConfigHash(base) != computeConfigHash(withList) {
		t.Fatal("名单不应参与配置哈希")
	}

	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = base
	s.lastConfigHash = computeConfigHash(base)
	ctx := context.Background()

	s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncConfig, Config: withList})
	if k.reloadCalls != 0 || k.startCalls != 0 {
		t.Fatalf("只改名单不应重载内核: reload=%d start=%d", k.reloadCalls, k.startCalls)
	}
	if deviceip.Counts("203.0.114.9") {
		t.Fatal("推送后名单应立即生效")
	}

	s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncConfig, Config: base})
	if !deviceip.Counts("203.0.114.9") {
		t.Fatal("面板不再下发名单时应清空")
	}
	if k.reloadCalls != 0 || k.startCalls != 0 {
		t.Fatal("清空名单不应重载内核")
	}
}
