package directwg

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
)

// Only cores with a batch reader advertise the optional capability through Wrap.
type usageManaged struct {
	*Managed
	reader kernel.UsageReader
}

func (m *usageManaged) ReadUserUsage(ctx context.Context, ids []int) kernel.UsageSample {
	m.mu.Lock()
	defer m.mu.Unlock()
	proxyIDs := ids
	if m.direct {
		proxyIDs = nil
	}
	sample := m.reader.ReadUserUsage(ctx, proxyIDs)
	// Both ledgers survive a protocol change. Only the active protocol owns presence.
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if len(m.state.totals) > 0 && sample.Traffic.Users == nil {
		sample.Traffic.Users = make(map[int][2]int64, len(m.state.totals))
	}
	for id, wg := range m.state.totals {
		base := sample.Traffic.Users[id]
		sample.Traffic.Users[id] = [2]int64{base[0] + wg[0], base[1] + wg[1]}
	}
	if !m.direct {
		return sample
	}
	result := kernel.NewUsageSample(ids)
	result.Traffic = sample.Traffic
	now := time.Now()
	for id, user := range m.state.users {
		n := len(user.connections)
		result.Connections += n
		row := result.Users[id]
		if row != nil {
			row.Count = n
		}
		if !validAt(user, now) {
			continue
		}
		m.state.pruneSourcesLocked(user, now)
		if len(user.sources) > 0 {
			result.OnlineUsers++
		}
		if row != nil {
			for source := range user.sources {
				row.AddAddress(source)
			}
		}
	}
	return result
}
