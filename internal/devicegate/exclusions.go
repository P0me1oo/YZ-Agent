package devicegate

import "github.com/P0me1oo/YZ-Agent/internal/deviceip"

// RetireExcluded cancels pending admissions and releases cached slots for sources
// which are now exempt. Callers rebuild connections on exclusion changes, so
// removing an exemption cannot leave existing connections uncounted indefinitely.
func (m *Manager) RetireExcluded(entries []string) {
	if m == nil {
		return
	}
	filter := &deviceip.Filter{}
	filter.SetExcluded(entries)
	var permits []*Permit
	m.mu.Lock()
	m.exclusionFilter = filter
	m.seq++
	m.exclusionBarrier = m.seq
	for key, flight := range m.flights {
		if filter.CountKey(key.ip) == "" {
			flight.abandoned = true
			flight.cancel()
		}
	}
	for _, source := range m.sources {
		if filter.CountKey(source.key.ip) != "" {
			continue
		}
		source.revoked = true
		if source.expiry != nil {
			source.expiry.Stop()
		}
		if len(source.permits) == 0 {
			m.removeSourceLocked(source)
		}
		for p := range source.permits {
			permits = append(permits, p)
		}
	}
	m.mu.Unlock()
	revokePermits(permits)
}
