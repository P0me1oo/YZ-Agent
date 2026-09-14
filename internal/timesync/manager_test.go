package timesync

import (
	"context"
	"errors"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestManagerCheckUsesMedianOffset(t *testing.T) {
	manager := New(config.TimeSyncConfig{
		Servers: []string{"one.example", "two.example", "three.example"},
	})
	offsets := map[string]time.Duration{
		"one.example":   20 * time.Second,
		"two.example":   1 * time.Second,
		"three.example": 3 * time.Second,
	}
	manager.exchange = func(_ context.Context, _ N.Dialer, address M.Socksaddr) (*ntp.Response, error) {
		return validResponse(offsets[address.Fqdn]), nil
	}

	snapshot, err := manager.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if snapshot.OffsetMS != (3 * time.Second).Milliseconds() {
		t.Fatalf("offset_ms = %d, want %d", snapshot.OffsetMS, (3 * time.Second).Milliseconds())
	}
	if snapshot.Source != "three.example" {
		t.Fatalf("source = %q, want three.example", snapshot.Source)
	}
	if snapshot.Status != StatusNormal {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusNormal)
	}
	if snapshot.Samples != 3 {
		t.Fatalf("samples = %d, want 3", snapshot.Samples)
	}
}

func TestManagerRejectsInvalidResponses(t *testing.T) {
	manager := New(config.TimeSyncConfig{Servers: []string{"bad.example"}})
	manager.exchange = func(context.Context, N.Dialer, M.Socksaddr) (*ntp.Response, error) {
		return &ntp.Response{Stratum: 16}, nil
	}

	snapshot, err := manager.Check(context.Background())
	if err == nil {
		t.Fatal("Check() error = nil, want invalid response error")
	}
	if snapshot.Status != StatusUnavailable {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusUnavailable)
	}
	if snapshot.LastError == "" {
		t.Fatal("last_error should record the failed query")
	}
}

func TestManagerRetainsFreshOffsetAfterQueryFailure(t *testing.T) {
	manager := New(config.TimeSyncConfig{Servers: []string{"time.example"}})
	manager.exchange = func(context.Context, N.Dialer, M.Socksaddr) (*ntp.Response, error) {
		return validResponse(8 * time.Second), nil
	}
	if _, err := manager.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager.exchange = func(context.Context, N.Dialer, M.Socksaddr) (*ntp.Response, error) {
		return nil, errors.New("network unavailable")
	}

	snapshot, err := manager.Check(context.Background())
	if err == nil {
		t.Fatal("Check() error = nil, want network error")
	}
	if snapshot.Status != StatusWarning {
		t.Fatalf("status = %q, want %q", snapshot.Status, StatusWarning)
	}
	if snapshot.OffsetMS != (8 * time.Second).Milliseconds() {
		t.Fatalf("offset_ms = %d, want retained offset", snapshot.OffsetMS)
	}
}

func TestManagerTimeFuncFallsBackWhenCalibrationIsStale(t *testing.T) {
	manager := New(config.TimeSyncConfig{Interval: 1})
	manager.clock.Store(&clockState{
		offset:      time.Hour,
		lastSuccess: time.Now().Add(-4 * time.Second),
	})
	manager.mu.Lock()
	manager.state.lastSuccess = time.Now().Add(-4 * time.Second)
	manager.state.offset = time.Hour
	manager.mu.Unlock()

	now := time.Now()
	got := manager.TimeFunc()()
	if delta := got.Sub(now); delta < -time.Second || delta > time.Second {
		t.Fatalf("stale TimeFunc() delta = %v, want system time", delta)
	}
	if snapshot := manager.Snapshot(); snapshot.Status != StatusUnavailable || !snapshot.Stale {
		t.Fatalf("snapshot = %+v, want stale unavailable", snapshot)
	}
}

func TestManagerUsageControlsRequiredState(t *testing.T) {
	manager := New(config.TimeSyncConfig{})
	manager.SetUsage("node-1", true)
	if !manager.Snapshot().Required {
		t.Fatal("required = false after adding SS2022 consumer")
	}
	manager.SetUsage("node-1", false)
	if manager.Snapshot().Required {
		t.Fatal("required = true after removing the only consumer")
	}
}

func TestManagerStartStopWaitsForInitialCheck(t *testing.T) {
	manager := New(config.TimeSyncConfig{
		Servers:  []string{"time.example"},
		Interval: 3600,
	})
	manager.exchange = func(ctx context.Context, _ N.Dialer, _ M.Socksaddr) (*ntp.Response, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return validResponse(0), nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	cancel()
	manager.Stop()
	// Stop 应保持幂等；父上下文已取消时也应安全退出。
	manager.Stop()
}

func TestManagerClassifyThresholds(t *testing.T) {
	manager := New(config.TimeSyncConfig{})
	tests := map[time.Duration]Status{
		4 * time.Second:   StatusNormal,
		5 * time.Second:   StatusWarning,
		15 * time.Second:  StatusError,
		25 * time.Second:  StatusCritical,
		-30 * time.Second: StatusCritical,
	}
	for offset, want := range tests {
		if got := manager.classify(offset); got != want {
			t.Errorf("classify(%v) = %q, want %q", offset, got, want)
		}
	}
}

func validResponse(offset time.Duration) *ntp.Response {
	now := time.Now()
	return &ntp.Response{
		Time:          now,
		ClockOffset:   offset,
		Stratum:       1,
		ReferenceTime: now.Add(-time.Second),
	}
}
