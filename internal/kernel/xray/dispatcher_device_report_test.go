package xray

import (
	"slices"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestLimitDispatcherReportsDeviceLimit(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 2}, nil)
	cl := limiter.New()
	cl.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 2}})
	ld.SetConnLimiter(cl)

	for _, ip := range []string{"1.1.1.1", "2.2.2.2"} {
		if ld.checkDeviceLimit(email, ip, true) {
			t.Fatalf("名额未满时 %s 应放行", ip)
		}
	}
	// 已登记的来源再次连接不算超限。
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("已登记来源应继续放行")
	}
	for _, ip := range []string{"3.3.3.3", "3.3.3.3", "4.4.4.4"} {
		if !ld.checkDeviceLimit(email, ip, true) {
			t.Fatalf("名额已满时新来源 %s 应被拒", ip)
		}
	}

	stat, ok := cl.SnapshotLimitEvents()[1]
	if !ok {
		t.Fatal("设备超限应记入本周期统计")
	}
	if stat.DeviceHits != 3 || stat.DeviceLimit != 2 || stat.PeakDevices != 2 {
		t.Fatalf("设备超限统计 = (%d, %d, %d)，期望 (3, 2, 2)", stat.DeviceHits, stat.DeviceLimit, stat.PeakDevices)
	}
	if !slices.Equal(stat.DeviceIPs, []string{"3.3.3.3", "4.4.4.4"}) {
		t.Fatalf("被拒来源 = %v", stat.DeviceIPs)
	}
}

func TestLimitDispatcherDeviceReportCountsGlobalSources(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 2}, nil)
	ld.UpdateGlobalDevices(map[int][]string{1: {"9.9.9.9"}}, time.Now())
	cl := limiter.New()
	cl.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 2}})
	ld.SetConnLimiter(cl)

	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("第二个名额应放行")
	}
	if !ld.checkDeviceLimit(email, "2.2.2.2", true) {
		t.Fatal("本节点与其他节点合计已满，新来源应被拒")
	}
	if stat := cl.SnapshotLimitEvents()[1]; stat.PeakDevices != 2 {
		t.Fatalf("已计入来源数 = %d，期望包含其他节点的来源共 2 个", stat.PeakDevices)
	}
}

func TestLimitDispatcherDeviceLimitWithoutReporter(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)
	// 只实现连接准入接口的 limiter 不支持设备上报，拒绝逻辑照常。
	ld.SetConnLimiter(&stubConnLimiter{allowRate: true})

	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("第一个来源应放行")
	}
	if !ld.checkDeviceLimit(email, "2.2.2.2", true) {
		t.Fatal("名额已满时新来源应被拒")
	}
}
