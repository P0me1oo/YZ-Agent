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

func TestDisabledHostMonitoringDoesNotCallSystemCollector(t *testing.T) {
	oldCollect, oldExpiry, oldStatus := systemSampler.collect, systemSampler.expires, systemSampler.status
	defer func() {
		systemSampler.collect, systemSampler.expires, systemSampler.status = oldCollect, oldExpiry, oldStatus
	}()
	calls := 0
	systemSampler.expires = time.Time{}
	systemSampler.collect = func() Status { calls++; return Status{CPU: 42} }
	for range 3 {
		s := CollectWithHost(false)
		if calls != 0 || !s.HostSkipped || s.CPU != 0 || s.Goroutines == 0 {
			t.Fatal("Lite 模式仍采样系统，或丢失进程运行信息")
		}
	}
	if s := CollectWithHost(true); s.HostSkipped || s.CPU != 42 || calls != 1 {
		t.Fatal("关闭 Lite 后未恢复原采样")
	}
	if s := CollectWithHost(false); !s.HostSkipped || s.CPU != 0 || calls != 1 {
		t.Fatal("重新启用 Lite 后仍读取主机缓存")
	}
}
