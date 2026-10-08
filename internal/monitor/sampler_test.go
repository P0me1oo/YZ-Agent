package monitor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedSamplerCollectsOncePerSecondAndIsolatesCallerSlices(t *testing.T) {
	var clock, calls atomic.Int64
	clock.Store(time.Now().UnixNano())
	s := sampler{interval: time.Second, now: func() time.Time { return time.Unix(0, clock.Load()) }, collect: func() Status {
		n := calls.Add(1)
		return Status{CPU: float64(n), CPUPerCore: []float64{20, 30}, NetInSpeed: 100}
	}}
	var workers sync.WaitGroup
	for range 100 {
		workers.Go(func() {
			status := s.sample()
			if status.CPU != 1 || status.CPUPerCore[0] != 20 || status.NetInSpeed != 100 {
				t.Error("并发节点没有取得同一份系统采样")
			}
			status.CPUPerCore[0] = 99
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("一秒内重复采集了 %d 次", calls.Load())
	}
	clock.Add(int64(time.Second))
	if status := s.sample(); status.CPU != 2 || status.CPUPerCore[0] != 20 || calls.Load() != 2 {
		t.Fatal("缓存到期必须重新采集，调用方修改不得污染共享内容")
	}
}
