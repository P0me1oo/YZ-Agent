package service

import (
	"math"
	"time"
)

// userSpeedSampler 只保留上一份累计值和当前网速，不参与计费和落盘。
// 由 Service 主循环串行调用；每次生成新 map，发送中的快照不会被下一次采样修改。
type userSpeedSampler struct {
	previous map[int][2]int64
	at       time.Time
	rates    map[int][2]int64
	maxGap   time.Duration
	reset    []int
	scoped   bool
}

func (s *userSpeedSampler) sample(totals map[int][2]int64, now time.Time) {
	s.sampleSelection(totals, now, false)
}

// The scoped map belongs to the sampler and includes explicit zero counters for watched users.
func (s *userSpeedSampler) sampleSelection(totals map[int][2]int64, now time.Time, scoped bool) {
	previous, elapsed := s.previous, now.Sub(s.at)
	wasScoped := s.scoped
	s.scoped = scoped
	ready := !s.at.IsZero() && elapsed > 0 && elapsed <= max(s.maxGap, 5*time.Second)
	if scoped {
		s.previous = totals
	} else {
		s.previous = make(map[int][2]int64, len(totals))
	}
	s.at, s.rates = now, nil
	s.reset = s.reset[:0]
	rates := make(map[int][2]int64)
	valid := len(totals) == 0
	for id, total := range totals {
		if !scoped {
			s.previous[id] = total
		}
		before, existed := previous[id]
		if !existed && (scoped || wasScoped) {
			continue
		}
		if total[0] < 0 || total[1] < 0 || before[0] < 0 || before[1] < 0 || total[0] < before[0] || total[1] < before[1] {
			s.reset = append(s.reset, id)
			continue
		}
		if !ready {
			continue
		}
		valid = true
		// 使用实际经过的时间；不应用套餐倍率或累计流量上报周期。
		value := [2]int64{
			int64(math.Min(9007199254740991, float64(total[0]-before[0])/elapsed.Seconds())),
			int64(math.Min(9007199254740991, float64(total[1]-before[1])/elapsed.Seconds())),
		}
		if scoped || value[0] > 0 || value[1] > 0 {
			rates[id] = value
		}
	}
	if ready && valid {
		s.rates = rates
	}
}
