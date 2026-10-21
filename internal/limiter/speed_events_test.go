package limiter

import (
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"testing"
	"time"
)

func TestSpeedEventsRequireActualDebtAndRespectReload(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "test-speed", SpeedLimit: 1}})
	tracker := NewSpeedTracker(l)
	tracker.UpdateBuckets()
	bucket := tracker.GetStableLimiter("test-speed")
	now := time.Now()
	tracker.SampleThrottled(now)
	if len(l.SnapshotLimitEvents()) != 0 {
		t.Fatal("configured limit is not a throttle event")
	}
	_ = bucket.ReserveN(now, bucket.Burst())
	_ = bucket.ReserveN(now, bucket.Burst())
	tracker.SampleThrottled(now)
	stat := l.SnapshotLimitEvents()[1]
	if stat.SpeedHits != 1 || stat.SpeedLimit != 1 || stat.SpeedFirst != now.Unix() {
		t.Fatal("actual debt must be sampled")
	}
	if len(l.SnapshotLimitEvents()) != 0 {
		t.Fatal("snapshot must clear only acknowledged collection")
	}
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "test-speed", SpeedLimit: 0}})
	tracker.UpdateBuckets()
	tracker.SampleThrottled(now)
	if len(l.SnapshotLimitEvents()) != 0 {
		t.Fatal("disabled speed limit must not produce events")
	}
}
