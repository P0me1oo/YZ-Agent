package panel

import (
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/realityscan"
	"github.com/gofrs/uuid/v5"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMachineControlExchangesRealityWithoutChangingOperations(t *testing.T) {
	id, _ := uuid.NewV4()
	server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Supported bool               `json:"reality_scan_supported"`
			Scan      realityscan.Report `json:"reality_scan"`
			Operation MachineOperation   `json:"operation"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !body.Supported || body.Scan.ID != id.String() || body.Scan.Status != "running" || body.Operation.Action != "restart" {
			t.Errorf("错误的控制报告：%+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"command":      MachineOperation{ID: id.String(), Action: "restart", Status: "pending"},
			"reality_scan": realityscan.Task{ID: id.String(), Targets: []string{"example.com"}, ExpiresAt: 1, Diagnostics: true},
		})
	})
	defer server.Close()
	operation, scan, err := client.ExchangeMachineControlWithReality("v2.12.0", "test-process", true,
		&MachineOperation{Action: "restart"}, &realityscan.Report{ID: id.String(), Status: "running"})
	if err != nil || operation.Action != "restart" || scan.ID != id.String() || !scan.Diagnostics {
		t.Fatalf("控制与检测必须同时保留：%+v %+v %v", operation, scan, err)
	}
}

func TestMachineControlAuthenticationAndRuntime(t *testing.T) {
	credential, _ := uuid.NewV4()
	id, _ := uuid.NewV4()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v2/server/machine/control" || r.URL.RawQuery != "" {
			t.Error("unexpected request path")
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["token"] != credential.String() || body["machine_id"] != float64(7) {
			t.Error("missing machine auth")
		}
		if body["version"] != "v1.16.0" || body["boot_id"] != "process-start" || body["manageable"] != true {
			t.Error("incorrect runtime report")
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"command": MachineOperation{ID: id.String(), Action: "restart", Status: "pending"}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, Token: credential.String(), MachineID: 7})
	command, err := client.ExchangeMachineControl("v1.16.0", "process-start", true, nil)
	if err != nil || command == nil || command.ID != id.String() || command.Action != "restart" {
		t.Fatalf("unexpected command: %+v, %v", command, err)
	}
}

func TestMachineControlOldPanelAndErrors(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		command, err := client.ExchangeMachineControl("v1.16.0", "process-start", false, nil)
		server.Close()
		if command != nil || (code == 404 && err != nil) || (code != 404 && err == nil) {
			t.Fatalf("status %d: %v", code, err)
		}
	}
}

func TestMachineControlReportsUpgradeResultWithoutChangingProcess(t *testing.T) {
	for _, result := range []string{"updated", "up_to_date", "current_newer"} {
		t.Run(result, func(t *testing.T) {
			server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					BootID    string           `json:"boot_id"`
					Operation MachineOperation `json:"operation"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.BootID != "same-process" || body.Operation.Status != "succeeded" || body.Operation.Result != result {
					t.Errorf("unexpected report: %+v", body)
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"command": nil})
			})
			defer server.Close()
			id, _ := uuid.NewV4()
			command, err := client.ExchangeMachineControl("v1.16.1", "same-process", true, &MachineOperation{ID: id.String(), Action: "upgrade", Status: "succeeded", Result: result})
			if err != nil || command != nil {
				t.Fatalf("command=%+v err=%v", command, err)
			}
		})
	}
}
