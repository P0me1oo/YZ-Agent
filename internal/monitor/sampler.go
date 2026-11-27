package monitor

import (
	"slices"
	"sync"
	"time"
)

// 共用进程快照，避免多个逻辑节点在同一秒重复触发运行时统计。
var processSampler = sampler{now: time.Now, collect: processStatus, interval: time.Second}
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
