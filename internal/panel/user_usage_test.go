package panel

import (
	"reflect"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestUserUsageLeaseIsolationAndBounds(t *testing.T) {
	c := NewClient(config.PanelConfig{NodeID: 1})
	d := TelemetryDemand{UserUsageVersion: 1, UserUsageIDs: []int{3, 2, 3}, OmitDeviceDetails: true, DetailInterval: 60, LeaseSeconds: 60}
	c.acceptTelemetry(&d, StatePayload{})
	d.UserUsageIDs[1] = 99
	ids, ok := c.UserUsageDemand()
	if !ok || !reflect.DeepEqual(ids, []int{2, 3}) {
		t.Fatal(ids, ok)
	}
	copy := *c.realtime.telemetry.Load()
	copy.until = time.Now().Add(-time.Second)
	c.realtime.telemetry.Store(&copy)
	if _, ok := c.UserUsageDemand(); ok {
		t.Fatal("expired lease exposed addresses")
	}
	for _, bad := range [][]int{{-1}, make([]int, 65)} {
		d.UserUsageIDs = bad
		c.acceptTelemetry(&d, StatePayload{})
		if _, ok := c.UserUsageDemand(); ok {
			t.Fatal("bad lease accepted")
		}
	}
	d.UserUsageIDs = nil
	c.acceptTelemetry(&d, StatePayload{})
	if ids, ok := c.UserUsageDemand(); !ok || len(ids) != 0 {
		t.Fatal("idle lease must remain compact")
	}
}
