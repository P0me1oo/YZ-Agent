package autospeed

import (
	"testing"
	"time"
)

func rule() *Rule {
	return &Rule{Enabled: true, Mode: "continuous", Direction: "download", Threshold: 10, Target: 2, Duration: 3, Hold: 10, Window: 10, Hits: 3}
}
func TestTriggerExpiryRestartAndRuleChange(t *testing.T) {
	r := rule()
	rules := map[int]*Rule{1: r}
	now := time.Unix(1000, 0)
	var m Manager
	rates := map[int][2]int64{1: {0, 2000000}}
	for i := 0; i < 3; i++ {
		if len(m.Update(rules, rates, now.Add(time.Duration(i)*time.Second))) > 0 {
			t.Fatal("early")
		}
	}
	p := m.Update(rules, rates, now.Add(3*time.Second))
	if p[1].Limit != 2 || p[1].Until != 1013 {
		t.Fatal(p)
	}
	var restored Manager
	restored.Restore(p, rules, now.Add(8*time.Second))
	if restored.Snapshot(now.Add(9 * time.Second))[1].Until != 1013 {
		t.Fatal("extended")
	}
	if len(restored.Update(rules, rates, now.Add(13*time.Second))) != 0 {
		t.Fatal("expiry must restore")
	}
	r.Target = 1
	if len(m.Update(rules, rates, now.Add(5*time.Second))) != 0 {
		t.Fatal("changed rule retained old penalty")
	}
}
func TestWindowDeduplicatesAndGapsReset(t *testing.T) {
	r := rule()
	r.Mode = "window"
	var m Manager
	rules := map[int]*Rule{1: r}
	rates := map[int][2]int64{1: {0, 2000000}}
	now := time.Unix(100, 0)
	for i := 0; i < 8; i++ {
		if len(m.Update(rules, rates, now)) != 0 {
			t.Fatal("duplicate triggered")
		}
	}
	if len(m.Update(rules, rates, now.Add(10*time.Second))) != 0 {
		t.Fatal("gap counted")
	}
	m.Update(rules, rates, now.Add(11*time.Second))
	if m.Update(rules, rates, now.Add(12*time.Second))[1].Limit != 2 {
		t.Fatal("window not triggered")
	}
	if len(m.Update(map[int]*Rule{}, rates, now.Add(13*time.Second))) != 0 {
		t.Fatal("deleted user retained")
	}
}
func TestMalformedAndLateRestore(t *testing.T) {
	r := rule()
	if !r.Valid() {
		t.Fatal()
	}
	r.Target = 20
	if r.Valid() {
		t.Fatal()
	}
	r.Target = 2
	var m Manager
	m.Restore(map[int]Penalty{1: {2, 500, Fingerprint(r)}}, map[int]*Rule{1: r}, time.Unix(100, 0))
	if len(m.Snapshot(time.Unix(100, 0))) > 0 {
		t.Fatal("invalid future expiry")
	}
}

func TestContinuousResetsOnLowSampleDespiteTickerJitter(t *testing.T) {
	r := rule()
	var m Manager
	rules := map[int]*Rule{1: r}
	start := time.Unix(1000, 0)
	for _, sample := range []struct {
		ms    int
		speed int64
	}{
		{0, 2000000}, {999, 0}, {2000, 2000000}, {3000, 2000000}, {4000, 2000000},
	} {
		if got := m.Update(rules, map[int][2]int64{1: {0, sample.speed}}, start.Add(time.Duration(sample.ms)*time.Millisecond)); len(got) != 0 {
			t.Fatalf("低速样本后不应提前处罚，时间 %d: %v", sample.ms, got)
		}
	}
	if got := m.Update(rules, map[int][2]int64{1: {0, 2000000}}, start.Add(5*time.Second)); got[1].Limit != 2 {
		t.Fatal("新的连续超阈区间未触发", got)
	}
}

func TestWindowCountsSecondsDespiteTickerJitter(t *testing.T) {
	r := rule()
	r.Mode, r.Window, r.Hits = "window", 3, 3
	var m Manager
	rules := map[int]*Rule{1: r}
	start := time.Unix(1000, 0)
	rates := map[int][2]int64{1: {0, 2000000}}
	for _, ms := range []int{1, 300, 900, 1000, 2001} {
		got := m.Update(rules, rates, start.Add(time.Duration(ms)*time.Millisecond))
		if (len(got) == 1) != (ms == 2001) {
			t.Fatalf("按秒计数错误，时间 %d: %v", ms, got)
		}
	}
}

func TestRepeatedUserSyncDoesNotReuseSpeedSample(t *testing.T) {
	r := rule()
	r.Mode, r.Window, r.Hits = "window", 10, 3
	var m Manager
	rules := map[int]*Rule{1: r}
	start := time.Unix(1000, 0)
	rates := map[int][2]int64{1: {0, 2000000}}
	for i := range 5 {
		if got := m.UpdateSample(rules, rates, start, start.Add(time.Duration(i)*time.Second)); len(got) != 0 {
			t.Fatal("同一份速度被重复计数", got)
		}
	}
	m.UpdateSample(rules, nil, start.Add(6*time.Second), start.Add(6*time.Second))
	if got := m.Update(rules, rates, start.Add(7*time.Second)); len(got) != 0 {
		t.Fatal("中断前的样本仍参与判定", got)
	}
}
