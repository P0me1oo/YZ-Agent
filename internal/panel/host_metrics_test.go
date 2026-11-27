package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestMachineHostMetricsNegotiationAndAcknowledgement(t *testing.T) {
	type request struct {
		Host      json.RawMessage   `json:"host_metrics"`
		Operation *MachineOperation `json:"operation"`
		Node      int               `json:"node_id"`
	}
	var requests []request
	var sizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/machine/control" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		var req request
		if err != nil || json.Unmarshal(raw, &req) != nil {
			t.Error("invalid control request")
			return
		}
		requests = append(requests, req)
		sizes = append(sizes, len(raw))
		switch len(requests) {
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 1, 3, 4:
			_, _ = io.WriteString(w, `{"host_metrics_version":1,"host_monitoring":false,"command":{"id":"control","action":"restart"}}`)
		default:
			_, _ = io.WriteString(w, `{"host_monitoring":false}`)
		}
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 1})
	operation := &MachineOperation{ID: "heartbeat", Action: "upgrade", Status: "succeeded"}
	// Negotiate, lose a response, retry, reuse the cached sample, roll back the panel.
	for i := 1; i <= 6; i++ {
		command, _, err := client.ExchangeMachineControlContext(context.Background(), "v2.53.0", "boot", true, operation, nil)
		if (i == 2) != (err != nil) {
			t.Fatalf("heartbeat %d error %v", i, err)
		}
		if (i == 1 || i == 3 || i == 4) && (command == nil || command.Action != "restart") {
			t.Fatalf("heartbeat %d lost the command", i)
		}
	}
	if len(requests) != 6 {
		t.Fatalf("got %d requests for 6 control heartbeats", len(requests))
	}
	for i, req := range requests {
		want := i == 1 || i == 2
		if (req.Host != nil) != want || req.Node != 0 || req.Operation == nil || req.Operation.ID != "heartbeat" {
			t.Fatalf("heartbeat %d machine metrics or operation mismatch", i+1)
		}
	}
	if string(requests[1].Host) != string(requests[2].Host) {
		t.Fatal("failed request did not retry the same cached sample")
	}
	var fields map[string]*float64
	if err := json.Unmarshal(requests[2].Host, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sampled_at", "cpu_percent", "memory_used_bytes", "memory_total_bytes", "disk_used_bytes", "disk_total_bytes", "upload_bytes_per_second", "download_bytes_per_second"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("wire field %s missing", key)
		}
	}
	if fields["sampled_at"] == nil || *fields["sampled_at"] <= 0 {
		t.Fatal("sample time missing")
	}
	if client.hostMetricsEnabled.Load() {
		t.Fatal("panel rollback did not disable the stream")
	}
	t.Logf("control body %d bytes, with sample %d bytes (%d additional), six heartbeats and six requests", sizes[0], sizes[2], sizes[2]-sizes[0])
}
