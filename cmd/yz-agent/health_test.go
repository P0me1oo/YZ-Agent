package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/service"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

func TestHealthTrackerLifecycle(t *testing.T) {
	tracker := newHealthTracker()
	tracker.reset([]string{"machine"})

	assertHealth := func(wantCode int, wantStatus string) {
		t.Helper()
		response := httptest.NewRecorder()
		tracker.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != wantCode {
			t.Fatalf("status code = %d, want %d; body=%s", response.Code, wantCode, response.Body.String())
		}
		if got := tracker.snapshot().Status; got != wantStatus {
			t.Fatalf("health status = %q, want %q", got, wantStatus)
		}
	}

	assertHealth(http.StatusServiceUnavailable, "starting")
	tracker.set("machine", service.RuntimeRunning)
	assertHealth(http.StatusOK, "ok")
	tracker.set("machine", service.RuntimeFailed)
	assertHealth(http.StatusServiceUnavailable, "degraded")
	tracker.set("machine", service.RuntimeStarting)
	assertHealth(http.StatusServiceUnavailable, "starting")
	tracker.set("machine", service.RuntimeRunning)
	assertHealth(http.StatusOK, "ok")
}

func TestHealthClockDegradationDoesNotReturnServiceUnavailable(t *testing.T) {
	tracker := newHealthTracker()
	tracker.reset([]string{"node"})
	tracker.set("node", service.RuntimeRunning)

	enabled := false
	clock := timesync.New(config.TimeSyncConfig{Enabled: &enabled})
	clock.SetUsage("node", true)
	tracker.setClock(clock)

	response := httptest.NewRecorder()
	tracker.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	snapshot := tracker.snapshot()
	if snapshot.Status != "degraded" {
		t.Fatalf("health status = %q, want degraded", snapshot.Status)
	}
	if !snapshot.Clock.Required || snapshot.Clock.Status != timesync.StatusDisabled {
		t.Fatalf("clock snapshot = %+v", snapshot.Clock)
	}
}
