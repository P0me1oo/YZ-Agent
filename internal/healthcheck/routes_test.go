package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowRouteDoesNotBlockLaterHealthyRoutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	release, healthy := make(chan struct{}), make(chan struct{})
	done := make(chan []Route, 1)
	go func() {
		done <- CheckRoutes(ctx, map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}, func(ctx context.Context, tag string) (int64, error) {
			if tag == "a" {
				select {
				case <-release:
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			}
			if tag == "e" {
				close(healthy)
			}
			if tag == "b" {
				return 5, errors.New("真实连接失败")
			}
			return 5, nil
		})
	}()
	select {
	case <-healthy:
	case <-ctx.Done():
		close(release)
		t.Fatal("后面的健康线路被慢线路阻塞")
	}
	close(release)
	routes := <-done
	if routes[0].Status != "ok" || routes[1].Status != "failed" || routes[4].Status != "ok" {
		t.Fatalf("未完成、实际失败与成功应分开: %+v", routes)
	}
}

func TestDeadlineLeavesQueuedRoutesIncompleteAndBoundsConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	targets := map[string]int{}
	for i := range 17 {
		targets[fmt.Sprintf("route-%02d", i)] = i
	}
	started := make(chan struct{}, 17)
	var active, peak atomic.Int32
	done := make(chan []Route, 1)
	go func() {
		done <- CheckRoutes(ctx, targets, func(ctx context.Context, tag string) (int64, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); old < n && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-ctx.Done()
			return 1, ctx.Err()
		})
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("检查未并发启动")
		}
	}
	cancel()
	routes := <-done
	if peak.Load() > 4 || len(routes) != 17 {
		t.Fatal("并发上限或结果数量错误", peak.Load(), len(routes))
	}
	for _, route := range routes {
		if route.Status != "incomplete" {
			t.Fatal("超时线路误标为故障", route)
		}
	}
}
