package tracker

import "testing"

func BenchmarkReviewSteadyTenThousandUsers(b *testing.B) {
	tracker := New()
	traffic := make(map[int][2]int64, 10000)
	for id := 1; id <= 10000; id++ {
		traffic[id] = [2]int64{1, 1}
	}
	tracker.Process(traffic, nil, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		tracker.Process(traffic, nil, 0)
	}
}
