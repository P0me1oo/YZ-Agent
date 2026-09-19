package limiter

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestConnLimiterNoLimitsFastPath(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a"}})

	if l.hasConnLimits.Load() {
		t.Fatal("没有用户配置连接限制时应关闭快速路径标记")
	}
	if _, ok := l.MaxConnByUserID(1); ok {
		t.Fatal("未配置并发上限的用户不应返回上限")
	}
	if !l.AllowNewConn(1) {
		t.Fatal("未配置速率上限的用户必须恒放行")
	}
}

func TestConnLimiterMaxConn(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{
		{ID: 1, UUID: "a", ConnLimit: 5},
		{ID: 2, UUID: "b"},
	})

	limit, ok := l.MaxConnByUserID(1)
	if !ok || limit != 5 {
		t.Fatalf("MaxConnByUserID(1) = (%d, %v), 期望 (5, true)", limit, ok)
	}
	if _, ok := l.MaxConnByUserID(2); ok {
		t.Fatal("用户 2 没配并发上限，不应返回上限")
	}
	// 只配了并发上限，速率维度仍然放行。
	if !l.AllowNewConn(1) {
		t.Fatal("未配置速率上限时 AllowNewConn 必须放行")
	}
}

func TestConnLimiterRateBucket(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", ConnRateLimit: 3}})

	// 桶容量等于每秒额度，前 3 次放行，第 4 次在同一瞬间必然被拒。
	for i := 0; i < 3; i++ {
		if !l.AllowNewConn(1) {
			t.Fatalf("第 %d 次新建应在桶容量内", i+1)
		}
	}
	if l.AllowNewConn(1) {
		t.Fatal("超出桶容量的新建应被拒绝")
	}
}

func TestConnLimiterRateBucketSurvivesUserRefresh(t *testing.T) {
	l := New()
	users := []model.UserSpec{{ID: 1, UUID: "a", ConnRateLimit: 2}}
	l.UpdateUsers(users)

	for i := 0; i < 2; i++ {
		if !l.AllowNewConn(1) {
			t.Fatalf("第 %d 次新建应在桶容量内", i+1)
		}
	}

	// 配置刷新不能重建令牌桶，否则被限的用户立刻拿到一次完整突发额度。
	l.UpdateUsers(users)
	if l.AllowNewConn(1) {
		t.Fatal("刷新用户后令牌桶不应被重置")
	}
}

func TestConnLimiterSnapshotLimitEvents(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", ConnLimit: 10, ConnRateLimit: 4}})

	l.ReportLimited(1, model.ConnLimitKindConcurrent, 10, 12)
	l.ReportLimited(1, model.ConnLimitKindConcurrent, 10, 11)
	// limit 传 0 时由 limiter 自己补全配置值。
	l.ReportLimited(1, model.ConnLimitKindRate, 0, 0)

	stats := l.SnapshotLimitEvents()
	stat, ok := stats[1]
	if !ok {
		t.Fatal("用户 1 应出现在快照里")
	}
	if stat.ConnHits != 2 || stat.RateHits != 1 {
		t.Fatalf("命中次数 = (%d, %d)，期望 (2, 1)", stat.ConnHits, stat.RateHits)
	}
	if stat.ConnLimit != 10 || stat.RateLimit != 4 {
		t.Fatalf("上限 = (%d, %d)，期望 (10, 4)", stat.ConnLimit, stat.RateLimit)
	}
	if stat.PeakConn != 12 {
		t.Fatalf("并发峰值 = %d，期望 12", stat.PeakConn)
	}
	if got := l.SnapshotMetrics().ConnLimitEvents; got != 3 {
		t.Fatalf("累计超限次数 = %d，期望 3", got)
	}

	// 快照即清零，下一个周期没有新事件时不应重复上报。
	if again := l.SnapshotLimitEvents(); len(again) != 0 {
		t.Fatalf("第二次快照应为空，实际 %#v", again)
	}
}

func TestConnLimiterDropsRemovedUsers(t *testing.T) {
	l := New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "a", ConnLimit: 10}})
	l.ReportLimited(1, model.ConnLimitKindConcurrent, 10, 12)

	l.UpdateUsers([]model.UserSpec{{ID: 2, UUID: "b", ConnLimit: 10}})

	if _, ok := l.MaxConnByUserID(1); ok {
		t.Fatal("已移除的用户不应保留并发上限")
	}
	if stats := l.SnapshotLimitEvents(); len(stats) != 0 {
		t.Fatalf("已移除用户的统计应被清理，实际 %#v", stats)
	}
}
