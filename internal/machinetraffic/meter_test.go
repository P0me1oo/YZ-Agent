package machinetraffic

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testPolicy(t *testing.T) Policy {
	t.Helper()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	start, end, err := Period(now, 31, "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return Policy{Limit: 100, Mode: "outbound", ResetDay: 31, Timezone: "Asia/Shanghai", Revision: 1, Epoch: 1, PeriodStart: start, PeriodEnd: end, Active: true}
}

func TestMeterBaselineRestartResetAndBilling(t *testing.T) {
	p := testPolicy(t)
	now := time.Unix(p.PeriodStart+3600, 0)
	reading := Reading{Boot: "boot-a", Counters: map[string]Counter{"eth0": {Identity: "2", RX: 1000, TX: 2000}}}
	reader := func([]string) (Reading, error) { return reading, nil }
	path := filepath.Join(t.TempDir(), "meter.json")
	m := New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	s, blocked := m.Tick(now)
	if blocked || s.RX != 0 || s.TX != 0 {
		t.Fatalf("initial baseline: %+v, %v", s, blocked)
	}
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 1040, TX: 2060}}
	s, blocked = m.Tick(now.Add(time.Second))
	if blocked || s.RX != 40 || s.TX != 60 {
		t.Fatalf("outbound: %+v, %v", s, blocked)
	}
	p.Mode = "both"
	p.Revision++
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	if _, blocked = m.Snapshot(); !blocked {
		t.Fatal("bidirectional quota ignored")
	}
	p.Limit = 200
	p.Revision++
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	if _, blocked = m.Snapshot(); blocked {
		t.Fatal("raised quota did not recover")
	}
	// 重启从磁盘继续，面板滞后值不能覆盖本地已计入用量。
	m = New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 1050, TX: 2070}}
	s, _ = m.Tick(now.Add(2 * time.Second))
	if s.RX != 50 || s.TX != 70 {
		t.Fatalf("restart lost data: %+v", s)
	}
	// 单个计数归零只累计新计数，不产生无符号减法溢出。
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 3, TX: 2080}}
	s, _ = m.Tick(now.Add(3 * time.Second))
	if s.RX != 53 || s.TX != 80 {
		t.Fatalf("counter reset: %+v", s)
	}
	reading.Boot = "boot-b"
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 7, TX: 9}}
	s, _ = m.Tick(now.Add(4 * time.Second))
	if s.RX != 60 || s.TX != 89 {
		t.Fatalf("OS reboot: %+v", s)
	}
}

func TestMeterRolloverOldRepliesAndStateLoss(t *testing.T) {
	p := testPolicy(t)
	reading := Reading{Boot: "boot-a", Counters: map[string]Counter{"eth0": {Identity: "2", RX: 1, TX: 1}}}
	reader := func([]string) (Reading, error) { return reading, nil }
	path := filepath.Join(t.TempDir(), "meter.json")
	m := New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	m.Tick(time.Unix(p.PeriodEnd-2, 0))
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 20, TX: 201}}
	_, blocked := m.Tick(time.Unix(p.PeriodEnd-1, 0))
	if !blocked {
		t.Fatal("quota did not stop service")
	}
	reading.Counters = map[string]Counter{"eth0": {Identity: "2", RX: 21, TX: 202}}
	s, blocked := m.Tick(time.Unix(p.PeriodEnd, 0))
	if blocked || s.TX != 1 || s.PeriodStart != p.PeriodEnd {
		t.Fatalf("rollover: %+v %v", s, blocked)
	}
	p.RX, p.TX, p.Initialized = 19, 200, true
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	s, blocked = m.Snapshot()
	if blocked || s.TX != 1 {
		t.Fatal("old reply carried old usage into new period")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	m = New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	s, blocked = m.Tick(time.Unix(p.PeriodEnd-1, 0))
	if !blocked || s.Error != "state_lost" {
		t.Fatal("missing state bypassed protection")
	}
	// 状态丢失只能在确定的新周期重新建立基线。
	s, blocked = m.Tick(time.Unix(p.PeriodEnd, 0))
	if blocked || s.Error != "" {
		t.Fatalf("new cycle did not recover: %+v", s)
	}
}

func TestMeterFailuresAndUnlimited(t *testing.T) {
	p := testPolicy(t)
	now := time.Unix(p.PeriodStart+100, 0)
	path := filepath.Join(t.TempDir(), "meter.json")
	readErr := errors.New("unavailable")
	reader := func([]string) (Reading, error) {
		return Reading{Boot: "test-boot", Counters: map[string]Counter{"eth0": {Identity: "2"}}}, readErr
	}
	m := New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	s, blocked := m.Tick(now)
	if !blocked || s.Error != "interfaces_unavailable" {
		t.Fatal("unknown interface did not fail closed")
	}
	readErr = nil
	if _, blocked = m.Tick(now); blocked {
		t.Fatal("interface recovery failed")
	}
	// 写入目标成为目录，故障不能被当成成功采样。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, blocked = m.Tick(now)
	if !blocked || s.Error != "state_write_failed" {
		t.Fatal("failed write did not fail closed")
	}
	p.Limit = 0
	p.Revision++
	_ = m.Apply(p)
	if _, blocked = m.Tick(now); blocked {
		t.Fatal("unlimited server affected by metering failure")
	}
}

func TestPeriodClampsMonthEndAndUsesTimezoneMidnight(t *testing.T) {
	for _, test := range []struct{ now, start, end string }{
		{"2026-02-28T00:00:00+08:00", "2026-02-28T00:00:00+08:00", "2026-03-31T00:00:00+08:00"},
		{"2028-02-29T00:00:00+08:00", "2028-02-29T00:00:00+08:00", "2028-03-31T00:00:00+08:00"},
		{"2026-02-27T23:59:59+08:00", "2026-01-31T00:00:00+08:00", "2026-02-28T00:00:00+08:00"},
	} {
		parse := func(s string) time.Time {
			v, e := time.Parse(time.RFC3339, s)
			if e != nil {
				t.Fatal(e)
			}
			return v
		}
		a, b, e := Period(parse(test.now), 31, "Asia/Shanghai")
		if e != nil || a != parse(test.start).Unix() || b != parse(test.end).Unix() {
			t.Fatalf("period %s: %d %d %v", test.now, a, b, e)
		}
	}
	before, _ := time.Parse(time.RFC3339, "2026-03-15T00:00:00-04:00")
	a, b, e := Period(before, 15, "America/New_York")
	if e != nil || a != before.Unix() || b <= a {
		t.Fatal("DST boundary failed")
	}
}

func TestMeterConcurrentSamplesAndPolicyUpdatesPersistOneState(t *testing.T) {
	p := testPolicy(t)
	reader := func([]string) (Reading, error) {
		return Reading{Boot: "test-boot", Counters: map[string]Counter{"eth0": {Identity: "2", RX: 20, TX: 40}}}, nil
	}
	path := filepath.Join(t.TempDir(), "meter.json")
	m := New(path, reader)
	if err := m.Apply(p); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range 10 {
				if worker%2 == 0 {
					m.Tick(time.Unix(p.PeriodStart+100, 0))
				} else {
					next := p
					next.Revision = uint64(i + 2)
					if err := m.Apply(next); err != nil {
						t.Error(err)
					}
				}
			}
		}()
	}
	workers.Wait()
	s, blocked := m.Snapshot()
	saved := New(path, reader)
	copy, restoredBlocked := saved.Snapshot()
	if blocked || restoredBlocked || s.Sequence != 20 || s.Sequence != copy.Sequence || s.RX != copy.RX || s.TX != copy.TX {
		t.Fatal("concurrent updates lost persisted metering state")
	}
}
