package panel

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/binding"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/realityscan"
	"github.com/gofrs/uuid/v5"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
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

func TestMachineControlDeletionRequiresProofForThisHeartbeat(t *testing.T) {
	key := rand.Text()
	var mode atomic.Value
	var mu sync.Mutex
	var previous string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		scenario := mode.Load().(string)
		var heartbeat struct{ Nonce string }
		if err := json.NewDecoder(r.Body).Decode(&heartbeat); err != nil || len(heartbeat.Nonce) != 64 || heartbeat.Nonce == previous {
			t.Error("missing fresh challenge")
			return
		}
		nonce := heartbeat.Nonce
		defer func() { previous = heartbeat.Nonce }()
		switch scenario {
		case "403", "404":
			code, _ := strconv.Atoi(scenario)
			w.WriteHeader(code)
			return
		case "old-panel":
			fmt.Fprint(w, `{"command":null}`)
			return
		case "replay":
			nonce = previous
		}
		credential := key
		if scenario == "rotated" {
			credential = rand.Text()
		}
		id := 17
		if scenario == "other-machine" {
			id++
		}
		hash := sha256.Sum256([]byte(credential))
		mac := hmac.New(sha256.New, hash[:])
		fmt.Fprintf(mac, "yz-agent-machine-deleted-v1\n%d\n%s", id, nonce)
		json.NewEncoder(w).Encode(map[string]any{"binding_deletion": binding.Proof{Version: 1, MachineID: id, State: "deleted", Nonce: nonce, MAC: hex.EncodeToString(mac.Sum(nil))}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 17, Token: key})
	var proof binding.Confirmation
	client.SetBindingDeletionHandler(func(confirmed binding.Confirmation) { proof = confirmed })
	for _, name := range []string{"valid", "replay", "rotated", "other-machine", "old-panel", "403", "404"} {
		t.Run(name, func(t *testing.T) {
			mode.Store(name)
			proof = binding.Confirmation{}
			cmd, _ := client.ExchangeMachineControl("test", "boot", false, nil)
			if cmd != nil || proof.Matches(server.URL, 17, key) != (name == "valid") {
				t.Fatal("incorrect deletion decision")
			}
			if proof.Matches(server.URL, 18, key) || proof.Matches(server.URL, 17, "replacement") || proof.Matches(server.URL+"/other", 17, key) {
				t.Fatal("confirmation matched another binding")
			}
		})
	}
}
