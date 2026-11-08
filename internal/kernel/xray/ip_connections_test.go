package xray

import (
	"context"
	"testing"
)

func TestIPConnectionsMergeDrainingInstances(t *testing.T) {
	current, previous := newTestDispatcher(), newTestDispatcher()
	email := userEmail(1)
	for _, d := range []*LimitDispatcher{current, previous} {
		d.UpdateLimits(map[string]int{email: 1}, nil, nil)
		for _, ip := range []string{"8.8.8.8", "8.8.8.8", "2606:4700:1:2::1", "2606:4700:1:2::2", "127.0.0.1"} {
			if limited := d.checkDeviceLimit(email, ip, true); limited {
				t.Fatal("连接被意外拒绝")
			}
		}
	}
	core := &Xray{limitDispatcher: current, retired: []*retiredXray{{dispatcher: previous}}}
	counts, err := core.GetIPConnectionSnapshot(context.Background())
	if err != nil || counts[1]["8.8.8.8"] != 4 || len(counts[1]) != 3 || counts[1]["2606:4700:1:2::1"] != 2 {
		t.Fatalf("热重载连接汇总错误: %v %v", counts, err)
	}
	for _, d := range []*LimitDispatcher{current, previous} {
		for _, ip := range []string{"8.8.8.8", "8.8.8.8", "2606:4700:1:2::1", "2606:4700:1:2::2", "127.0.0.1"} {
			d.delConn(email, ip)
		}
	}
	cleared, _ := core.GetIPConnectionSnapshot(context.Background())
	if cleared == nil || len(cleared) != 0 || counts[1]["8.8.8.8"] != 4 {
		t.Fatalf("关闭回收或快照隔离失败: %v", cleared)
	}
}
