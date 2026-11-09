package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

type brokenSnapshotKernel struct {
	*fakeKernel
	failure error
}

func (k *brokenSnapshotKernel) GetConnectionSnapshot(context.Context) (map[int]int, map[int]map[int]int, error) {
	return nil, nil, k.failure
}
func (k *brokenSnapshotKernel) GetIPConnectionSnapshot(context.Context) (map[int]map[string]int, error) {
	return nil, k.failure
}
func (k *brokenSnapshotKernel) GetRelayTraffic(context.Context) (map[int][2]int64, error) {
	return nil, k.failure
}

func TestSamplingFailureVisibleAtNormalLevelAndRateLimited(t *testing.T) {
	nlog.ConfigureLogPolicies(nil)
	nlog.Init(io.Discard, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	var events []nlog.Event
	stop := nlog.Subscribe(t.Name(), func(e nlog.Event) { events = append(events, e) })
	defer stop()
	k := &brokenSnapshotKernel{fakeKernel: &fakeKernel{}, failure: errors.New("sampling token=test-secret")}
	s := &Service{kernel: k, tracker: tracker.New(), nodeLog: nlog.Scoped(t.Name(), 7, "node:7")}
	for i := 0; i < 10; i++ {
		s.trackConnectionCounts(context.Background())
		s.trackRelayTraffic(context.Background())
	}
	if len(events) != 3 {
		t.Fatalf("expected one warning per operation, got %d", len(events))
	}
	for _, e := range events {
		if e.Level != "warn" || e.NodeID != 7 || strings.Contains(e.Message, "test-secret") {
			t.Fatal("invalid diagnostic", e)
		}
	}
	k.failure = nil
	s.trackConnectionCounts(context.Background())
	k.failure = errors.New("new failure")
	s.trackConnectionCounts(context.Background())
	if len(events) != 5 {
		t.Fatal("failure after recovery was suppressed")
	}
}
