package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"slices"
	"testing"
)

func TestConnectionSnapshotExcludesRejectedRegistrationAndKeepsExistingSameIP(t *testing.T) {
	d := newTestDispatcher()
	email := userEmail(1)
	d.UpdateLimits(map[string]int{email: 1}, nil, nil)
	l := limiter.New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 1}})
	d.SetConnLimiter(l)
	d.checkDeviceLimit(email, "198.51.100.1", true)
	d.userConnCounter(1).Store(1)
	for _, source := range []string{"203.0.113.1", "198.51.100.1"} {
		d.checkDeviceLimit(email, source, true)
		_, _, _, _, rejected := d.checkConnGate(email, source)
		d.delConn(email, source)
		if !rejected {
			t.Fatal("满额请求未拒绝")
		}
		snapshot := l.SnapshotLimitEvents()[1].ConnSnapshot
		if snapshot == nil || snapshot.SourceIP != source || !slices.Equal(snapshot.ActiveIPs, []string{"198.51.100.1"}) {
			t.Fatalf("来源快照错误：%+v", snapshot)
		}
	}
}
