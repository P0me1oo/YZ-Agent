package limiter

import (
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"testing"
)

func TestTemporaryUsesStableBucketAndRestoresCurrentBase(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "test", SpeedLimit: 100}})
	s := NewSpeedTracker(l)
	s.UpdateBuckets()
	bucket := s.GetStableLimiter("test")
	s.SetTemporary(map[int]int{1: 20})
	if s.GetStableLimiter("test") != bucket || float64(bucket.Limit()) != 2500000 {
		t.Fatal("not applied in place")
	}
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "test", SpeedLimit: 10}})
	s.UpdateBuckets()
	if float64(bucket.Limit()) != 1250000 {
		t.Fatal("temporary raised base")
	}
	s.SetTemporary(nil)
	if float64(bucket.Limit()) != 1250000 {
		t.Fatal("restored old base")
	}
}
