package service

import (
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func (s *Service) userUsageDemand() ([]int, bool) {
	if _, ok := s.kernel.(kernel.UsageReader); !ok {
		return nil, false
	}
	p, ok := s.sink.(interface{ UserUsageDemand() ([]int, bool) })
	if !ok || !s.realtimeEnabled() {
		return nil, false
	}
	return p.UserUsageDemand()
}

func (s *Service) usageDetailsDue() bool {
	if s.stateActive.Load() {
		return false
	}
	sender, ok := s.sink.(interface{ StateDue() bool })
	return !ok || sender.StateDue()
}

func (s *Service) sampleRequestedUsage(ids []int, sample kernel.UsageSample, now time.Time) {
	// Enforcement runs every sampling cycle even when the panel has no subscribers.
	if len(ids) == 0 && len(s.autoSpeed.rules) == 0 {
		s.userSpeed = userSpeedSampler{maxGap: s.userSpeed.maxGap, scoped: true}
		return
	}
	selected := make(map[int][2]int64, len(ids)+len(s.autoSpeed.rules))
	for id := range s.autoSpeed.rules {
		selected[id] = sample.Traffic.Users[id]
	}
	for _, id := range ids {
		selected[id] = sample.Traffic.Users[id]
	}
	s.userSpeed.sampleSelection(selected, now, true)
	s.updateAutoSpeed(now)
	if len(sample.Users) == 0 {
		return
	}
	rows := make(map[int]panel.UserUsage, len(ids))
	running := s.kernel.IsRunning() && s.runtimeError == nil
	for _, id := range ids {
		row := panel.UserUsage{}
		if running && s.limiter.HasUser(id) {
			if conns := sample.Users[id]; conns != nil {
				row.IPs = conns.Addresses()
				row.Connections = &conns.Count
			}
			if rate, ok := s.userSpeed.rates[id]; ok {
				row.Upload = &rate[0]
				row.Download = &rate[1]
			}
			row.LimitKnown = s.autoSpeed.loaded && s.autoSpeed.error == ""
			if p, ok := s.autoSpeed.active[id]; row.LimitKnown && ok && p.Until > now.Unix() {
				row.TemporaryLimit = &panel.TemporaryLimit{Mbps: p.Limit, Until: p.Until}
			}
		}
		rows[id] = row
	}
	s.usageBatch = &panel.UserUsageBatch{SampledAt: now.Unix(), Users: rows}
}
