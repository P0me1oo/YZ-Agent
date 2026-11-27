package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// Eight logical nodes share one machine. Each has 128 online users. Measure
// ticks between two HTTP fallback sends, with the real client and no WS.
func BenchmarkMonitorHTTPFallbackTick(b *testing.B) {
	var posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/handshake"):
			_, _ = w.Write([]byte(`{"host_monitoring":false,"realtime":{"version":1,"traffic_ack":true}}`))
		case strings.HasSuffix(r.URL.Path, "/begin"):
			_, _ = w.Write([]byte(`{"data":{"epoch":"bench"}}`))
		default:
			var state panel.StateVersion
			if err := json.NewDecoder(r.Body).Decode(&state); err != nil {
				b.Error(err)
				return
			}
			posts.Add(1)
			receipt := panel.RPCReceipt{StateVersion: state, Accepted: true,
				Telemetry: &panel.TelemetryDemand{DetailInterval: 60, LeaseSeconds: 60}}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt})
		}
	}))
	defer server.Close()
	machines := panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1})
	if _, err := machines.Handshake(); err != nil {
		b.Fatal(err)
	}
	services := make([]*Service, 8)
	addresses := make(map[int]map[string]bool, 128)
	for i := 1; i <= 128; i++ {
		addresses[i] = map[string]bool{"8.8.8.8": true}
	}
	publish := func(s *Service) {
		s.publishRuntimeState(context.Background())
		for s.stateActive.Load() {
			runtime.Gosched()
		}
	}
	for i := range services {
		child := machines.ForNode(i + 1)
		if _, err := child.Handshake(); err != nil {
			b.Fatal(err)
		}
		cp := controlplane.NewMachinePanelControlPlane(child, nil, nil)
		s, _ := newShutdownReportService()
		s.source, s.sink = cp, cp
		s.tracker.Process(nil, addresses, 128)
		publish(s)
		services[i] = s
	}
	if posts.Load() != int64(len(services)) {
		b.Fatal("initial state not delivered")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, s := range services {
			publish(s)
		}
	}
	b.StopTimer()
	if extra := posts.Load() - int64(len(services)); extra != 0 {
		b.Fatalf("expected fallback window, saw %d extra HTTP requests; use -benchtime=100x", extra)
	}
}
