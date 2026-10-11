package singbox

import (
	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"slices"
	"testing"
)

func TestConnectionSnapshotPreservesExistingSourcesAndRateRejection(t *testing.T) {
	tk := NewConnTracker(0)
	tk.SetUserMap(map[string]int{"snapshot-test": 1})
	l := limiter.New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 1, ConnRateLimit: 1}})
	tk.SetConnLimiter(l)
	us := tk.users[1]
	us.addConn("198.51.100.1")
	if _, _, _, rejected := tk.checkConnGate(us, 1, "203.0.113.1"); !rejected {
		t.Fatal("满额请求未拒绝")
	}
	snapshot := l.SnapshotLimitEvents()[1].ConnSnapshot
	if snapshot == nil || snapshot.SourceIP != "203.0.113.1" || !slices.Equal(snapshot.ActiveIPs, []string{"198.51.100.1"}) {
		t.Fatalf("来源快照错误：%+v", snapshot)
	}
	l.UpdateUsers([]model.UserSpec{{ID: 1, ConnRateLimit: 1}})
	if !l.AllowNewConn(1) {
		t.Fatal("首次令牌应可用")
	}
	if _, _, _, rejected := tk.checkConnGate(us, 1, "203.0.113.2"); !rejected {
		t.Fatal("速率超限未拒绝")
	}
	snapshot = l.SnapshotLimitEvents()[1].RateSnapshot
	if snapshot == nil || snapshot.SourceIP != "203.0.113.2" || snapshot.Limit != 1 || !slices.Equal(snapshot.ActiveIPs, []string{"198.51.100.1"}) {
		t.Fatalf("速率快照错误：%+v", snapshot)
	}
}
