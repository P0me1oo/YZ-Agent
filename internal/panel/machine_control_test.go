package panel

import (
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/gofrs/uuid/v5"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
