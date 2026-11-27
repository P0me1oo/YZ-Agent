package devicegate

import (
	"testing"
	"time"
)

func TestExcludedSourceRetiresIdleAndActiveLeases(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{true: "active", false: "idle"}[active], func(t *testing.T) {
			lease := testLease(1, "8.8.8.8", time.Hour)
			remote := &leaseRemote{leases: []Lease{lease}}
			m := cachedManager(t, remote)
			if err := m.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			p := acquire(t, m, 1, "8.8.8.8")
			if !active {
				p.Release()
			} else {
				p.Bind(p.Release)
			}
			m.RetireExcluded([]string{"8.8.8.8"})
			if active && p.Valid() {
				t.Fatal("old active lease survives exclusion change")
			}
			m.mu.Lock()
			count, retired := len(m.sources), len(m.retired)
			m.mu.Unlock()
			if count != 0 || retired != 1 {
				t.Fatalf("lease not retired: sources=%d retired=%d", count, retired)
			}
			if err := m.Sync(t.Context()); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			count = len(m.sources)
			m.mu.Unlock()
			if count != 0 {
				t.Fatal("stale reply resurrected excluded lease")
			}
			p, err := m.Acquire(t.Context(), 1, "8.8.8.8")
			if err != nil || p != nil {
				t.Fatal("excluded source requested a slot")
			}
			m.RetireExcluded(nil)
			if _, err := m.Acquire(t.Context(), 1, "8.8.8.8"); err == nil {
				t.Fatal("removed exclusion bypassed disconnected panel")
			}
		})
	}
}
