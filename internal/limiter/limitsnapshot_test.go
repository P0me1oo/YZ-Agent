package limiter

import (
	"slices"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestLimitSnapshotsKeepOneMatchingEventAcrossRetriesAndReset(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 2, ConnRateLimit: 3, DeviceLimit: 1}})
	for _, kind := range []string{model.ConnLimitKindConcurrent, model.ConnLimitKindRate, model.ConnLimitKindDevice} {
		first := model.NewLimitSnapshot("203.0.113.1", []string{"198.51.100.1"}, true)
		l.ReportLimitedSnapshot(1, kind, 0, 1, first.SourceIP, func() *model.LimitSnapshot { return first })
		first.ActiveIPs[0] = "198.51.100.99"
		l.ReportLimitedSnapshot(1, kind, 9, 9, "203.0.113.2", func() *model.LimitSnapshot {
			return model.NewLimitSnapshot("203.0.113.2", nil, true)
		})
	}
	stat := l.SnapshotLimitEvents()[1]
	for i, snapshot := range []*model.LimitSnapshot{stat.ConnSnapshot, stat.RateSnapshot, stat.DeviceSnapshot} {
		if snapshot == nil || snapshot.SourceIP != "203.0.113.1" || snapshot.Observed != 1 || snapshot.Limit != []int{2, 3, 1}[i] || !slices.Equal(snapshot.ActiveIPs, []string{"198.51.100.1"}) {
			t.Fatalf("快照与首次事件不一致：%+v", snapshot)
		}
	}
	if stat.ConnHits != 2 || stat.RateHits != 2 || stat.DeviceHits != 2 {
		t.Fatal("累计次数丢失")
	}
	if len(l.SnapshotLimitEvents()) != 0 {
		t.Fatal("上报后快照未清空")
	}
	l.UpdateUsers(nil)
	l.ReportLimitedSnapshot(1, model.ConnLimitKindDevice, 1, 1, "203.0.113.1", func() *model.LimitSnapshot { return model.NewLimitSnapshot("203.0.113.1", nil, true) })
	if len(l.SnapshotLimitEvents()) != 0 {
		t.Fatal("已删除用户被重新记录")
	}
}

func TestConcurrentLimitSnapshotFlushPreservesCountsAndSources(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 1}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				l.ReportLimitedSnapshot(1, model.ConnLimitKindConcurrent, 1, 1, "203.0.113.1", func() *model.LimitSnapshot {
					return model.NewLimitSnapshot("203.0.113.1", []string{"198.51.100.1"}, true)
				})
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var total uint64
	check := func() {
		for _, stat := range l.SnapshotLimitEvents() {
			total += stat.ConnHits
			if stat.ConnSnapshot == nil || stat.ConnSnapshot.SourceIP != "203.0.113.1" {
				t.Fatal("并发上报丢失来源")
			}
		}
	}
	for {
		check()
		select {
		case <-done:
			check()
			if total != 800 {
				t.Fatalf("次数 %d，期望 800", total)
			}
			return
		default:
		}
	}
}
