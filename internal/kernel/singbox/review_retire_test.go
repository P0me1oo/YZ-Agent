package singbox

import (
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

func TestHistoricalTrafficRetiresAfterConfirmationAndLateBytesStayIncremental(t *testing.T) {
	s := New(config.KernelConfig{})
	totals := tracker.New()
	for id := 1; id <= 1000; id++ {
		counter := s.traffic.user(id)
		counter.upload.Add(100)
		totals.Process(s.traffic.snapshot(), nil, 0)
		totals.RetireConfirmed(s.RetireTraffic)
		if len(s.traffic.users) != 1 {
			t.Fatal("未确认的流量被删除")
		}
		if got := totals.FlushTraffic()[id][0]; got != 100 {
			t.Fatalf("最终结算=%d", got)
		}
		totals.RetireConfirmed(s.RetireTraffic)
		if len(s.traffic.users) != 0 {
			t.Fatal("历史账本未回收")
		}
		// 旧零复制回调迟到；同 ID 已重新加入，新旧引用均归入新增量。
		s.traffic.user(id).upload.Add(7)
		counter.upload.Add(3)
		totals.Process(s.traffic.snapshot(), nil, 0)
		if got := totals.FlushTraffic()[id][0]; got != 10 {
			t.Fatalf("迟到流量丢失或重复=%d", got)
		}
		totals.RetireConfirmed(s.RetireTraffic)
	}
}

func TestConcurrentCounterRetirementPreservesAllBytes(t *testing.T) {
	s := New(config.KernelConfig{})
	counter := s.traffic.user(1)
	totals := tracker.New()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				counter.upload.Add(1)
			}
		})
	}
	var sum int64
	for range 100 {
		totals.Process(s.traffic.snapshot(), nil, 0)
		sum += totals.FlushTraffic()[1][0]
		totals.RetireConfirmed(s.RetireTraffic)
	}
	wg.Wait()
	totals.Process(s.traffic.snapshot(), nil, 0)
	sum += totals.FlushTraffic()[1][0]
	if sum != 4000 {
		t.Fatalf("并发回收丢失或重复字节=%d", sum)
	}
	s.traffic.user(1)
	s.users = []model.UserSpec{{ID: 1}}
	totals.RetireConfirmed(s.RetireTraffic)
	if len(s.traffic.users) == 0 {
		t.Fatal("当前用户被回收")
	}
}
