package singbox

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// 套餐限速变化后，已建立的连接必须立即使用新设置，而不是等用户重连。
func TestConnTrackerSpeedLimitChangesApplyToOpenConnections(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	var current atomic.Pointer[rate.Limiter]
	tracker.SetSpeedLimitFunc(func(uuid string) *rate.Limiter {
		if uuid != "uuid-1" {
			return nil
		}
		return current.Load()
	})

	tcp := tracker.RoutedConnection(context.Background(), &testConn{}, testInboundContext("uuid-1", "1.1.1.1"), nil, nil).(*trackedConn)
	udp := tracker.RoutedPacketConnection(context.Background(), &counterTestPacketConn{}, testInboundContext("uuid-1", "1.1.1.1"), nil, nil).(*trackedPacketConn)
	if tcp.currentLimiter() != nil || udp.currentLimiter() != nil {
		t.Fatal("不限速用户的连接不应带限速器")
	}

	limited := rate.NewLimiter(1000, 64*1024)
	current.Store(limited)
	tracker.RefreshSpeedLimits()
	if tcp.currentLimiter() != limited || udp.currentLimiter() != limited {
		t.Fatal("开始限速后已有连接应立即受限")
	}

	current.Store(nil)
	tracker.RefreshSpeedLimits()
	if tcp.currentLimiter() != nil || udp.currentLimiter() != nil {
		t.Fatal("取消限速后已有连接应立即恢复不限速")
	}
	_ = tcp.Close()
	_ = udp.Close()
}

func TestWaitRateSplitsPayloadLargerThanBurst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitRate(ctx, rate.NewLimiter(1000000, 64), 256); err != nil {
		t.Fatalf("超过单次突发额度的数据应分段等待: %v", err)
	}
}
