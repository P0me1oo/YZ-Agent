package service

import (
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"testing"
	"time"
)

func TestLimitOnlyBatchSurvivesRestartWithoutTraffic(t *testing.T) {
	dir := t.TempDir()
	s, _ := newPersistentReportService(t, dir)
	event := panel.LimitEvent{UserID: 7, Kind: "speed", Limit: 10, Count: 3, OccurredAt: time.Now().Unix()}
	s.retryReport = &reportBatch{id: "test-limit-batch", payload: controlplane.ReportPayload{ReportID: "test-limit-batch", LimitEvents: []panel.LimitEvent{event}}}
	if err := s.persistPending(); err != nil {
		t.Fatal(err)
	}
	restored, _ := newPersistentReportService(t, dir)
	restored.restorePendingReports()
	batch := restored.takeReportBatch()
	if batch.id != "test-limit-batch" || len(batch.payload.LimitEvents) != 1 || batch.payload.LimitEvents[0].Count != 3 {
		t.Fatal("limit batch must survive restart with original identity")
	}
}
