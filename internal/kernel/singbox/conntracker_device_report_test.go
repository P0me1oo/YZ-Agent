package singbox

import (
	"context"
	"slices"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestConnTrackerReportsDeviceLimit(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	cl := limiter.New()
	cl.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 1}})
	tracker.SetConnLimiter(cl)
	tracker.SetDeviceLimitFunc(cl.GetDeviceLimitByUUID)

	first := &testConn{}
	accepted := tracker.RoutedConnection(context.Background(), first, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)
	if accepted == first {
		t.Fatal("第一个来源应放行")
	}
	defer accepted.Close()

	for _, ip := range []string{"2.2.2.2", "2.2.2.2"} {
		base := &testConn{}
		if conn := tracker.RoutedConnection(context.Background(), base, testInboundContext("uuid-1", ip), nil, nil); conn != base {
			t.Fatalf("名额已满时新来源 %s 应被拒", ip)
		}
	}
	udp := &counterTestPacketConn{}
	if conn := tracker.RoutedPacketConnection(context.Background(), udp, testInboundContext("uuid-1", "3.3.3.3"), nil, nil); conn != udp {
		t.Fatal("UDP 新来源同样应被拒")
	}

	stat, ok := cl.SnapshotLimitEvents()[1]
	if !ok {
		t.Fatal("设备超限应记入本周期统计")
	}
	if stat.DeviceHits != 3 || stat.DeviceLimit != 1 || stat.PeakDevices != 1 {
		t.Fatalf("设备超限统计 = (%d, %d, %d)，期望 (3, 1, 1)", stat.DeviceHits, stat.DeviceLimit, stat.PeakDevices)
	}
	if !slices.Equal(stat.DeviceIPs, []string{"2.2.2.2", "3.3.3.3"}) {
		t.Fatalf("被拒来源 = %v", stat.DeviceIPs)
	}
	if stat.ConnHits != 0 || stat.RateHits != 0 {
		t.Fatalf("设备拒绝发生在连接准入之前，不应计入连接数次数：%#v", stat)
	}
}

func TestConnTrackerDeviceReportCountsGlobalSources(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	cl := limiter.New()
	cl.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "uuid-1", DeviceLimit: 2}})
	tracker.SetConnLimiter(cl)
	us := tracker.users[1]
	us.addConn("1.1.1.1")
	tracker.UpdateGlobalDevices(map[int][]string{1: {"9.9.9.9"}})

	if !tracker.checkDeviceGate(us, 1, "2.2.2.2", 2) {
		t.Fatal("本节点与其他节点合计已满，新来源应被拒")
	}
	if stat := cl.SnapshotLimitEvents()[1]; stat.PeakDevices != 2 || !slices.Equal(stat.DeviceIPs, []string{"2.2.2.2"}) {
		t.Fatalf("设备超限统计 = %#v，期望已计入 2 个来源", stat)
	}
}
