package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/machinetraffic"
)

func TestMachineTrafficProtocol(t *testing.T) {
	var reports []machinetraffic.Sample
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/server/machine/traffic" || r.Method != "POST" {
			t.Error("wrong traffic endpoint")
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var body struct {
			MachineID int                    `json:"machine_id"`
			Sample    *machinetraffic.Sample `json:"sample"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.MachineID != 7 {
			t.Error("machine identity missing")
		}
		if body.Sample != nil {
			reports = append(reports, *body.Sample)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"traffic": map[string]any{"limit": 100, "mode": "proxy", "proxy_rx": 20, "proxy_tx": 30, "epoch": 2, "revision": 3, "period_start": 1, "period_end": 200, "reset_day": 31, "timezone": "Asia/Shanghai", "active": true}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 7})
	p, err := client.ExchangeMachineTraffic(context.Background(), nil)
	if err != nil || p == nil || p.Limit != 100 || p.Mode != "proxy" || p.ProxyRX != 20 || p.ProxyTX != 30 {
		t.Fatalf("policy decode: %v", err)
	}
	sample := machinetraffic.Sample{Epoch: 2, Revision: 3, Mode: "proxy", PeriodStart: 1, Sequence: 4, RX: 20, TX: 30, Initialized: true, Interfaces: []string{}}
	for range 2 {
		if _, err := client.ExchangeMachineTraffic(context.Background(), &sample); err != nil {
			t.Fatal(err)
		}
	}
	if len(reports) != 2 || reports[0].Sequence != 4 || reports[1].RX != 20 || reports[1].Revision != 3 || reports[1].Mode != "proxy" {
		t.Fatal("retry changed report identity or counters")
	}
	status = http.StatusNotFound
	if p, err := client.ExchangeMachineTraffic(context.Background(), &sample); err == nil || p != nil {
		t.Fatal("unavailable protection API became an unlimited policy")
	}
}
