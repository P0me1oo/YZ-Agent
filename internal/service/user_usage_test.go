package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/autospeed"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type usageTestCore struct {
	*fakeKernel
	totals    map[int][2]int64
	requested []int
}

func (k *usageTestCore) ReadUserUsage(_ context.Context, ids []int) kernel.UsageSample {
	k.requested = ids
	sample := kernel.NewUsageSample(ids)
	sample.Traffic = model.TrafficCounters{Users: k.totals, Relay: map[int][2]int64{3: {99, 88}}, RelayUsers: map[int]map[int][2]int64{1: {3: {99, 88}}}}
	sample.OnlineUsers = 2
	sample.Connections = 5
	if row := sample.Users[1]; row != nil {
		row.Count = 3
		row.AddAddress("8.8.8.8")
		row.AddAddress("::ffff:8.8.8.8")
		row.AddAddress("127.0.0.1")
	}
	return sample
}

type usageTestPlane struct {
	*speedTestPublisher
	ids []int
	due bool
}

func (p *usageTestPlane) UserUsageDemand() ([]int, bool) { return p.ids, true }
func (p *usageTestPlane) StateDue() bool                 { return p.due }
func (p *usageTestPlane) TelemetryNeeds() (bool, bool)   { return false, false }

func TestUserUsageScopedBatchUnknownAndBilling(t *testing.T) {
	s, cp := newShutdownReportService()
	core := &usageTestCore{fakeKernel: &fakeKernel{running: true}, totals: map[int][2]int64{1: {2000, 5000}, 2: {4000, 8000}}}
	s.kernel = core
	s.prepareUserState([]model.UserSpec{{ID: 1}, {ID: 2}, {ID: 3}})
	p := &usageTestPlane{speedTestPublisher: &speedTestPublisher{shutdownReportPlane: cp, states: make(chan panel.StatePayload, 1)}, ids: []int{1, 3, 99}, due: true}
	s.sink = p
	sample := func() {
		t.Helper()
		if _, _, err := s.collectTraffic(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	sample()
	first := s.usageBatch
	row := first.Users[1]
	if row.Upload != nil || row.Connections == nil || *row.Connections != 3 || !reflect.DeepEqual(row.IPs, []string{"8.8.8.8"}) || !row.LimitKnown || row.TemporaryLimit != nil {
		t.Fatalf("bad first sample %+v", row)
	}
	if u := first.Users[99]; u.IPs != nil || u.Connections != nil || u.LimitKnown {
		t.Fatalf("unapplied user had a metric %+v", u)
	}
	core.totals[1] = [2]int64{2100, 5200}
	s.userSpeed.at = time.Now().Add(-time.Second)
	sample()
	row = s.usageBatch.Users[1]
	if row.Upload == nil || *row.Upload < 99 || *row.Upload > 100 || *row.Download < 199 || *row.Download > 200 {
		t.Fatalf("wrong rate %+v", row)
	}
	if idle := s.usageBatch.Users[3]; idle.Upload == nil || *idle.Upload != 0 || idle.IPs == nil || len(idle.IPs) != 0 || *idle.Connections != 0 {
		t.Fatalf("idle user %+v", idle)
	}
	s.publishRuntimeState(t.Context())
	state := <-p.states
	if state.Online != nil || state.OnlineCount == nil || *state.OnlineCount != 2 || state.UserUsage == nil || len(state.UserUsage.Users) != 3 || state.UserSpeeds != nil || state.ConnectionCounts != nil {
		t.Fatal("wrong batch", state)
	}
	if first.Users[1].Upload != nil {
		t.Fatal("published sample mutated")
	}
	for s.stateActive.Load() {
		time.Sleep(time.Millisecond)
	}
	core.totals[1] = [2]int64{1, 2}
	p.due = false
	sample()
	if len(core.requested) != 0 || s.usageBatch != nil {
		t.Fatal("built detailed IPs during transport backpressure")
	}
	p.ids = []int{1, 2, 3}
	p.due = true
	core.totals[2] = [2]int64{4500, 8500}
	sample()
	if s.usageBatch.Users[2].Upload != nil {
		t.Fatal("new subscription resurrected historical rate")
	}
	s.userSpeed.at = time.Now().Add(-time.Minute)
	sample()
	if s.usageBatch.Users[1].Upload != nil {
		t.Fatal("long gap produced a rate")
	}
	s.runtimeError = errors.New("test stopped")
	sample()
	if u := s.usageBatch.Users[1]; u.IPs != nil || u.Connections != nil || u.Upload != nil || u.LimitKnown {
		t.Fatal("stopped kernel invented an observation")
	}
	batch := s.takeReportBatch()
	if batch.payload.Traffic[1] != [2]int64{2101, 5202} || batch.payload.Traffic[2] != [2]int64{4500, 8500} || batch.payload.RelayUserTraffic[1][3] != [2]int64{99, 88} || batch.payload.RelayTraffic[3] != [2]int64{99, 88} || batch.payload.Metrics != nil {
		t.Fatalf("reliable report mismatch %+v", batch.payload)
	}
}

func TestUserUsageTemporaryLimitWithoutViewers(t *testing.T) {
	s, _ := newShutdownReportService()
	s.kernel = &fakeKernel{running: true}
	rule := &autospeed.Rule{Enabled: true, Mode: "continuous", Direction: "upload", Threshold: 10, Target: 2, Duration: 3, Hold: 10}
	s.prepareUserState([]model.UserSpec{{ID: 2, AutoSpeedLimit: rule}})
	t0 := time.Now()
	sample := func(i int, ids []int) {
		v := kernel.NewUsageSample(ids)
		v.Traffic.Users = map[int][2]int64{2: {int64(i) * 2_000_000, 0}}
		s.sampleRequestedUsage(ids, v, t0.Add(time.Duration(i)*time.Second))
	}
	for i := 0; i <= 4; i++ {
		sample(i, nil)
	}
	if s.usageBatch != nil || s.autoSpeed.active[2].Limit != 2 {
		t.Fatal("no viewer changed enforcement", s.autoSpeed.active)
	}
	sample(5, []int{2})
	u := s.usageBatch.Users[2]
	if !u.LimitKnown || u.TemporaryLimit == nil || u.TemporaryLimit.Mbps != 2 || u.TemporaryLimit.Until != t0.Unix()+14 {
		t.Fatal("missing original limit or expiry", u)
	}
	sample(14, []int{2})
	u = s.usageBatch.Users[2]
	if !u.LimitKnown || u.TemporaryLimit != nil || u.Upload != nil {
		t.Fatal("expired or gapped sample", u)
	}
}
