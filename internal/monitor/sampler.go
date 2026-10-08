package monitor

import (
	"slices"
	"sync"
	"time"
)

// 同一进程内的逻辑节点共用一秒采样，避免重复读取系统及互相扰动速率基线。
var systemSampler = sampler{now: time.Now, collect: collect, interval: time.Second}

type sampler struct {
	mu       sync.Mutex
	now      func() time.Time
	collect  func() Status
	interval time.Duration
	expires  time.Time
	status   Status
}

func (s *sampler) sample() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.expires.IsZero() || !now.Before(s.expires) {
		s.status = s.collect()
		s.expires = now.Add(s.interval)
	}
	result := s.status
	result.CPUPerCore = slices.Clone(result.CPUPerCore)
	return result
}
