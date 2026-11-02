package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
	"testing"
)

func TestXrayHistoricalLedgerRetirementKeepsNewBytesAndActiveUsers(t *testing.T) {
	x := New(config.KernelConfig{})
	totals := tracker.New()
	for id := 1; id <= 1000; id++ {
		x.cumTraffic[id] = [2]int64{100, 0}
		totals.Process(copyTraffic(x.cumTraffic), nil, 0)
		totals.RetireConfirmed(x.RetireTraffic)
		if len(x.cumTraffic) != 1 {
			t.Fatal("未确认累计被回收")
		}
		totals.FlushTraffic()
		totals.RetireConfirmed(x.RetireTraffic)
		if len(x.cumTraffic) != 0 {
			t.Fatal("历史累计未回收")
		}
		x.cumTraffic[id] = [2]int64{5, 0}
		totals.Process(copyTraffic(x.cumTraffic), nil, 0)
		if got := totals.FlushTraffic()[id][0]; got != 5 {
			t.Fatalf("迟到增量=%d", got)
		}
		totals.RetireConfirmed(x.RetireTraffic)
	}
	x.cumTraffic[1], x.users = [2]int64{1, 0}, []model.UserSpec{{ID: 1}}
	totals.Process(copyTraffic(x.cumTraffic), nil, 0)
	totals.FlushTraffic()
	totals.RetireConfirmed(x.RetireTraffic)
	if len(x.cumTraffic) != 1 {
		t.Fatal("当前用户被回收")
	}
}
