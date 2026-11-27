package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type roleMeterKernel struct {
	*fakeKernel
	raw, tail, relayed int64
	readErr            error
}

func (k *roleMeterKernel) Stop() {
	k.raw += k.tail
	k.tail = 0
	k.fakeKernel.Stop()
}

func (k *roleMeterKernel) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	return map[int][2]int64{1: {k.raw, 0}}, nil, 0, k.readErr
}

func (k *roleMeterKernel) GetRelayTraffic(context.Context) (map[int][2]int64, error) {
	return map[int][2]int64{-17: {k.relayed, 0}}, nil
}

func (k *roleMeterKernel) GetRelayUserTraffic(context.Context) (map[int]map[int][2]int64, error) {
	return map[int]map[int][2]int64{1: {8: {k.relayed, 0}}}, nil
}

func (k *roleMeterKernel) ReadUserUsage(ctx context.Context, ids []int) kernel.UsageSample {
	s := kernel.NewUsageSample(ids)
	s.Traffic.Users, _, _, _ = k.GetUserTraffic(ctx)
	s.Traffic.Relay, _ = k.GetRelayTraffic(ctx)
	s.Traffic.RelayUsers, _ = k.GetRelayUserTraffic(ctx)
	return s
}

func meterConfigs() (*model.NodeSpec, *model.NodeSpec) {
	direct := &model.NodeSpec{Protocol: "vless", ServerPort: 8443}
	entry := *direct
	entry.Relay = &model.RelayConfig{Mode: panel.RelayModeEntry, Accounting: "routes", RouteID: 3}
	return direct, &entry
}

func TestScopedTrafficSamplesRunningRoleAndFinalCounters(t *testing.T) {
	for _, compact := range []bool{false, true} {
		t.Run(map[bool]string{true: "compact", false: "ordinary"}[compact], func(t *testing.T) {
			s, cp := newShutdownReportService()
			core := &roleMeterKernel{fakeKernel: &fakeKernel{}}
			s.kernel = core
			if compact {
				s.sink = &usageTestPlane{speedTestPublisher: &speedTestPublisher{shutdownReportPlane: cp}}
			}
			s.lastUsers = []model.UserSpec{{ID: 1, UUID: "11111111-1111-1111-1111-111111111111"}}
			direct, entry := meterConfigs()
			apply := func(c *model.NodeSpec) {
				t.Helper()
				if !s.applyConfigUpdate(t.Context(), c, computeConfigHash(c)) {
					t.Fatalf("could not apply runtime: %v", s.runtimeError)
				}
			}
			sample := func() {
				t.Helper()
				if _, _, err := s.collectTraffic(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			apply(direct)
			core.raw, core.tail = 10, 5
			sample()
			apply(entry)
			core.raw += 20
			core.relayed = 11
			sample()
			core.tail = 3
			apply(direct)
			core.raw += 7
			sample()
			b := s.takeReportBatch().payload
			if !b.ScopedTraffic || b.DirectTraffic[1][0] != 22 || b.EntryTraffic[1][0] != 23 || b.Traffic[1][0] != 45 ||
				b.RelayTraffic[-17][0] != 11 || b.RelayUserTraffic[1][8][0] != 11 {
				t.Fatalf("wrong direct, entry or relay counters: %+v", b)
			}
		})
	}
}

func TestScopedTrafficFailedRoleBoundaryRetainsOldRole(t *testing.T) {
	for _, failure := range []string{"read", "persist"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := newPersistentReportService(t, t.TempDir())
			core := &roleMeterKernel{fakeKernel: &fakeKernel{}, raw: 21, tail: 4}
			s.kernel = core
			s.lastUsers = []model.UserSpec{{ID: 1, UUID: "11111111-1111-1111-1111-111111111111"}}
			direct, entry := meterConfigs()
			if !s.applyConfigUpdate(t.Context(), direct, computeConfigHash(direct)) {
				t.Fatal(s.runtimeError)
			}
			if failure == "read" {
				core.readErr = errors.New("read failure")
			} else {
				s.pendingStore.loadErr = errors.New("storage unavailable")
			}
			if s.applyConfigUpdate(t.Context(), entry, computeConfigHash(entry)) || core.IsRunning() || !s.meterSwitchPending {
				t.Fatal("started a new role before the old counters were durable")
			}
			core.readErr, s.pendingStore.loadErr = nil, nil
			s.trackAndEnforce(t.Context())
			if !core.IsRunning() || s.meterSwitchPending {
				t.Fatal("did not recover the same desired role")
			}
			core.raw += 30
			if _, _, err := s.collectTraffic(t.Context()); err != nil {
				t.Fatal(err)
			}
			b := s.takeReportBatch().payload
			if b.DirectTraffic[1][0] != 25 || b.EntryTraffic[1][0] != 30 {
				t.Fatal("recovery changed the billing role", b.DirectTraffic, b.EntryTraffic)
			}
		})
	}
}

func TestScopedTrafficRestartKeepsLegacyAndMixedBatchesSeparate(t *testing.T) {
	dir := t.TempDir()
	first, _ := newPersistentReportService(t, dir)
	old := pendingReportFile{Version: 1, Identity: first.pendingStore.identity,
		Batch:   &pendingBatch{ID: "already-sent", pendingTraffic: pendingTraffic{Traffic: map[int][2]int64{1: {4, 5}}}},
		Pending: pendingTraffic{Traffic: map[int][2]int64{1: {6, 7}}}}
	raw, _ := json.Marshal(old)
	if err := first.pendingStore.writeProtected(first.pendingStore.path, raw); err != nil {
		t.Fatal(err)
	}
	first.restorePendingReports()
	first.tracker.ProcessSummary(map[int][2]int64{1: {9, 12}}, 0, 0)
	if err := first.persistPending(); err != nil {
		t.Fatal(err)
	}
	first.tracker.SetEntryRuntime(true)
	first.tracker.ProcessSummary(map[int][2]int64{1: {15, 20}}, 0, 0)
	if err := first.persistPending(); err != nil {
		t.Fatal(err)
	}
	second, _ := newPersistentReportService(t, dir)
	second.restorePendingReports()
	batch := second.takeReportBatch()
	if batch.id != old.Batch.ID || batch.payload.ScopedTraffic || batch.payload.Traffic[1] != [2]int64{4, 5} {
		t.Fatal("old sent batch changed", batch)
	}
	second.forgetCompletedReport(batch)
	batch = second.takeReportBatch()
	if batch.id == old.Batch.ID || batch.payload.ScopedTraffic || batch.payload.Traffic[1] != [2]int64{6, 7} {
		t.Fatal("unclassified bytes mixed with new samples", batch)
	}
	third, _ := newPersistentReportService(t, dir)
	third.restorePendingReports()
	resent := third.takeReportBatch()
	if resent.id != batch.id || resent.payload.ScopedTraffic || resent.payload.Traffic[1] != [2]int64{6, 7} {
		t.Fatal("legacy retry did not retain its immutable identity")
	}
	third.forgetCompletedReport(resent)
	mixed := third.takeReportBatch().payload
	if !mixed.ScopedTraffic || mixed.DirectTraffic[1] != [2]int64{9, 12} || mixed.EntryTraffic[1] != [2]int64{6, 8} || mixed.Traffic[1] != [2]int64{15, 20} {
		t.Fatal("pending journal lost the sample roles", mixed)
	}
	fourth, _ := newPersistentReportService(t, dir)
	fourth.restorePendingReports()
	retry := fourth.takeReportBatch().payload
	if retry.ReportID != mixed.ReportID || !retry.ScopedTraffic || retry.DirectTraffic[1] != mixed.DirectTraffic[1] || retry.EntryTraffic[1] != mixed.EntryTraffic[1] {
		t.Fatal("scoped retry changed after restart", retry)
	}
}

func TestScopedTrafficUnreadableRoleEvidencePreservesCheckpoint(t *testing.T) {
	s, _ := newPersistentReportService(t, t.TempDir())
	invalid := pendingReportFile{Version: 2, Identity: s.pendingStore.identity,
		Pending: pendingTraffic{Scoped: true, Direct: map[int][2]int64{1: {1, 0}}, Traffic: map[int][2]int64{1: {4, 0}}}}
	raw, _ := json.Marshal(invalid)
	if err := s.pendingStore.writeProtected(s.pendingStore.path, raw); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.pendingStore.path)
	if err != nil {
		t.Fatal(err)
	}
	s.restorePendingReports()
	if err := s.persistPending(); err == nil {
		t.Fatal("inconsistent classified counters were overwritten")
	}
	after, err := os.ReadFile(s.pendingStore.path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed recovery changed the original checkpoint", err)
	}
}
