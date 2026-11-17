package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestAgentLogCrashCapabilityAndLegacyFallback(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(fmt.Sprint(capable), func(t *testing.T) {
			batch := &AgentLogBatch{ID: "test-batch", Records: []nlog.Event{{NodeID: 7, Source: "xray", Level: "error", Event: "xray.crash", Message: "listener failed"}}}
			reports := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/server/handshake" {
					events := []string{}
					if capable {
						events = append(events, "xray.crash")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"agent_log": map[string]any{"version": 1, "events": events}})
					return
				}
				var body struct {
					ID      string           `json:"batch_id"`
					Records []map[string]any `json:"records"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				_, hasEvent := body.Records[0]["event"]
				if hasEvent != capable || body.ID != batch.ID || body.Records[0]["node_id"] != float64(7) {
					t.Error("事件能力或节点归属错误")
				}
				reports++
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"batch_id": body.ID}})
			}))
			defer server.Close()
			client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7})
			if _, err := client.Handshake(); err != nil {
				t.Fatal(err)
			}
			if !client.AgentLogEnabled() {
				t.Fatal("原日志能力不应停用")
			}
			for i := 0; i < 2; i++ {
				if err := client.ReportAgentLog(context.Background(), batch); err != nil {
					t.Fatal(err)
				}
			}
			if reports != 2 || batch.Records[0].Event != "xray.crash" {
				t.Fatal("上报不能改写待确认批次")
			}
		})
	}
}

func TestAgentLogMinimumLevelAndLegacyFallback(t *testing.T) {
	var minimum atomic.Value
	minimum.Store("warn")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"agent_log": map[string]any{"version": 1, "min_level": minimum.Load()},
		})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7})
	for _, test := range []struct {
		name     string
		minimum  string
		allowed  []string
		rejected []string
	}{
		{name: "warnings", minimum: "warn", allowed: []string{"warn", "error", "fatal"}, rejected: []string{"debug", "info"}},
		{name: "errors", minimum: "error", allowed: []string{"error", "fatal"}, rejected: []string{"debug", "info", "warn"}},
		{name: "legacy_after_new_panel", allowed: []string{"debug", "info", "warn", "error", "fatal"}},
		{name: "unknown_extension", minimum: "future-level", allowed: []string{"debug", "info", "warn", "error", "fatal"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			minimum.Store(test.minimum)
			if _, err := client.HandshakeContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, level := range test.allowed {
				if !client.AgentLogLevelAllowed(level) {
					t.Errorf("required level %s was dropped", level)
				}
			}
			for _, level := range test.rejected {
				if client.AgentLogLevelAllowed(level) {
					t.Errorf("low level %s was retained", level)
				}
			}
		})
	}
}
