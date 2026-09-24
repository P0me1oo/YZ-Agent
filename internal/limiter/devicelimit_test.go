package limiter

import (
	"fmt"
	"slices"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestReportDeviceLimitedSnapshot(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", DeviceLimit: 2}})

	l.ReportDeviceLimited(1, 2, 2, "8.8.8.8")
	l.ReportDeviceLimited(1, 2, 3, "8.8.8.8")
	// limit 传 0 时由 limiter 按用户配置补全。
	l.ReportDeviceLimited(1, 0, 2, "8.8.4.4")

	stat, ok := l.SnapshotLimitEvents()[1]
	if !ok {
		t.Fatal("用户 1 应出现在快照里")
	}
	if stat.DeviceHits != 3 || stat.DeviceLimit != 2 || stat.PeakDevices != 3 {
		t.Fatalf("设备超限统计 = (%d, %d, %d)，期望 (3, 2, 3)", stat.DeviceHits, stat.DeviceLimit, stat.PeakDevices)
	}
	if !slices.Equal(stat.DeviceIPs, []string{"8.8.8.8", "8.8.4.4"}) {
		t.Fatalf("被拒来源 = %v，期望去重后保持首次出现顺序", stat.DeviceIPs)
	}
	if stat.ConnHits != 0 || stat.RateHits != 0 {
		t.Fatalf("设备超限不应计入连接数或速率次数：%#v", stat)
	}
	if got := l.SnapshotMetrics(); got.DeviceLimitEvents != 3 || got.ConnLimitEvents != 0 {
		t.Fatalf("累计指标 = %#v，期望设备 3 次、连接 0 次", got)
	}
	if again := l.SnapshotLimitEvents(); len(again) != 0 {
		t.Fatalf("第二次快照应为空，实际 %#v", again)
	}
}

func TestReportDeviceLimitedCapsSourceIPs(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", DeviceLimit: 1}})
	for i := 1; i <= MaxDeviceEventIPs+3; i++ {
		l.ReportDeviceLimited(1, 1, 1, fmt.Sprintf("203.0.113.%d", i))
	}

	stat := l.SnapshotLimitEvents()[1]
	if stat.DeviceHits != MaxDeviceEventIPs+3 {
		t.Fatalf("被拒次数 = %d，期望 %d", stat.DeviceHits, MaxDeviceEventIPs+3)
	}
	if len(stat.DeviceIPs) != MaxDeviceEventIPs || stat.DeviceIPs[0] != "203.0.113.1" {
		t.Fatalf("被拒来源 = %v，期望只保留前 %d 个", stat.DeviceIPs, MaxDeviceEventIPs)
	}
}

func TestReportDeviceLimitedIgnoresUnknownUser(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", DeviceLimit: 1}})
	l.ReportDeviceLimited(2, 1, 1, "8.8.8.8")

	if stats := l.SnapshotLimitEvents(); len(stats) != 0 {
		t.Fatalf("不在用户列表里的事件不应记录，实际 %#v", stats)
	}
	if got := l.SnapshotMetrics().DeviceLimitEvents; got != 0 {
		t.Fatalf("累计指标 = %d，期望 0", got)
	}
}

func TestReportDeviceLimitedMergesWithConnEvents(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", DeviceLimit: 1, ConnLimit: 4}})
	l.ReportLimited(1, model.ConnLimitKindConcurrent, 4, 4)
	l.ReportDeviceLimited(1, 1, 1, "8.8.8.8")

	stat := l.SnapshotLimitEvents()[1]
	if stat.ConnHits != 1 || stat.DeviceHits != 1 || stat.PeakConn != 4 || stat.PeakDevices != 1 {
		t.Fatalf("同一用户的连接与设备超限应各自累计：%#v", stat)
	}
}
