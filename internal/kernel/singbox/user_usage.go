package singbox

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
)

func (s *SingBox) ReadUserUsage(_ context.Context, ids []int) kernel.UsageSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sample := kernel.NewUsageSample(ids)
	sample.Traffic.Users = s.traffic.snapshot()
	sample.Traffic.Relay = s.traffic.relaySnapshot()
	sample.Traffic.RelayUsers = s.traffic.relayUserSnapshot()
	ct := s.connTracker
	if ct == nil {
		return sample
	}
	ct.usersMu.RLock()
	defer ct.usersMu.RUnlock()
	for uid, stats := range ct.users {
		stats.mu.RLock()
		if stats.connCount > 0 {
			sample.Connections += stats.connCount
			sample.OnlineUsers++
			if row := sample.Users[uid]; row != nil {
				row.Count = stats.connCount
				for ip, n := range stats.ips {
					if n > 0 {
						row.AddAddress(ip)
					}
				}
			}
		}
		stats.mu.RUnlock()
	}
	return sample
}
