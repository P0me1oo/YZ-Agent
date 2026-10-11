package directwg

import (
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestConnectionLimitSnapshotUsesTrackedSourcesAndReleasesThem(t *testing.T) {
	s := newState()
	u := stateUser(t)
	u.ConnLimit = 1
	s.replaceUsers([]model.UserSpec{u})
	l := limiter.New()
	l.UpdateUsers([]model.UserSpec{u})
	s.connLimiter = l
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_, release, ok := s.track(1, a, netip.MustParseAddr("198.51.100.1"))
	if !ok {
		t.Fatal("首条连接未放行")
	}
	if _, _, ok := s.track(1, b, netip.MustParseAddr("203.0.113.1")); ok {
		t.Fatal("超限连接未拒绝")
	}
	snapshot := l.SnapshotLimitEvents()[1].ConnSnapshot
	if snapshot == nil || snapshot.SourceIP != "203.0.113.1" || !slices.Equal(snapshot.ActiveIPs, []string{"198.51.100.1"}) {
		t.Fatalf("连接来源不匹配：%+v", snapshot)
	}
	release()
	release()
	if len(s.users[1].connectionIPs) != 0 {
		t.Fatal("重复释放后来源未清空")
	}
	_, release, ok = s.track(1, b, netip.MustParseAddr("203.0.113.1"))
	if !ok {
		t.Fatal("释放后无法恢复连接")
	}
	release()
}
