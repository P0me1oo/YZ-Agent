package singbox

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestConnTrackerConcurrentAdmission(t *testing.T) {
	for _, mode := range []string{"tcp", "udp", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			tracker := NewConnTracker(0)
			tracker.SetUserMap(map[string]int{"test-user": 1})
			cl := limiter.New()
			tracker.SetConnLimiter(cl)
			for round := 0; round < 3; round++ {
				cl.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 4}})
				tracker.SetUserMap(map[string]int{"test-user": 1})
				start := make(chan struct{})
				accepted := make(chan io.Closer, 64)
				var wg sync.WaitGroup
				for i := 0; i < 64; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						<-start
						metadata := testInboundContext("test-user", "192.0.2.1")
						if mode == "udp" || mode == "mixed" && i%2 == 0 {
							base := &counterTestPacketConn{}
							conn := tracker.RoutedPacketConnection(context.Background(), base, metadata, nil, nil)
							if conn != base {
								accepted <- conn
							}
						} else {
							base := &testConn{}
							conn := tracker.RoutedConnection(context.Background(), base, metadata, nil, nil)
							if conn != base {
								accepted <- conn
							} else if !base.closed {
								t.Error("拒绝后未关闭连接")
							}
						}
					}(i)
				}
				close(start)
				wg.Wait()
				close(accepted)
				if len(accepted) != 4 || tracker.ActiveCount() != 4 {
					t.Fatalf("放行 %d 条，计数 %d，期望 4", len(accepted), tracker.ActiveCount())
				}
				for conn := range accepted {
					_ = conn.Close()
					_ = conn.Close()
				}
				_, ips, count := tracker.GetUserTraffic()
				if count != 0 || len(ips) != 0 {
					t.Fatalf("关闭后未归零：%d %v", count, ips)
				}
				if stats := cl.SnapshotLimitEvents()[1]; stats.ConnHits != 60 || stats.PeakConn != 4 {
					t.Fatalf("拒绝统计错误：%+v", stats)
				}
			}
		})
	}
}

type admissionRateLimiter struct{ allow bool }

func (*admissionRateLimiter) MaxConnByUserID(int) (int, bool)     { return 1, true }
func (l *admissionRateLimiter) AllowNewConn(int) bool             { return l.allow }
func (*admissionRateLimiter) ReportLimited(int, string, int, int) {}

func TestConnTrackerRateRejectionDoesNotOccupySlot(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"test-user": 1})
	cl := &admissionRateLimiter{}
	tracker.SetConnLimiter(cl)
	metadata := testInboundContext("test-user", "192.0.2.1")
	base := &testConn{}
	if tracker.RoutedConnection(context.Background(), base, metadata, nil, nil) != base || !base.closed {
		t.Fatal("应拒绝速率超限连接")
	}
	_, ips, count := tracker.GetUserTraffic()
	if count != 0 || len(ips) != 0 {
		t.Fatal("速率拒绝占用了名额")
	}
	cl.allow = true
	conn := tracker.RoutedConnection(context.Background(), &testConn{}, metadata, nil, nil)
	if tracker.ActiveCount() != 1 {
		t.Fatal("恢复后应允许一条连接")
	}
	_ = conn.Close()
}
