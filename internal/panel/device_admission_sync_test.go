package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

func TestDeviceAdmissionSyncNegotiationAndPanelRollback(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(map[bool]string{true: "new", false: "legacy"}[supported], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/server/handshake" {
					realtime := map[string]int{"device_handover": 1}
					if supported {
						realtime["device_handover_admit_sync"] = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"realtime": realtime})
					return
				}
				var req map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&req)
				if supported {
					var snapshot devicegate.Snapshot
					if json.Unmarshal(req["snapshot"], &snapshot) != nil || snapshot.Sequence != 2 || snapshot.Pending == nil || snapshot.Sources == nil {
						t.Error("HTTP 请求未保留完整状态，包括空列表")
					}
				} else if _, ok := req["snapshot"]; ok {
					t.Error("旧面板请求增加了未协商字段")
				}
				// 模拟握手后回滚到旧面板，返回授权但忽略状态。
				_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.AdmissionReply{Status: "allowed", Lease: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}})
			}))
			defer server.Close()
			c := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7, Token: "admission-sync-test-only"})
			if _, err := c.Handshake(); err != nil || c.DeviceAdmissionSyncSupported() != supported {
				t.Fatal(err)
			}
			req := devicegate.AdmissionRequest{Run: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Sequence: 1, UserID: 1, IP: "8.8.8.8"}
			if supported {
				req.Snapshot = &devicegate.Snapshot{Run: req.Run, Sequence: 2, Pending: []uint64{1}, Sources: []devicegate.Source{}, Retired: []devicegate.Revocation{}}
			}
			reply, err := c.AdmitDeviceSource(t.Context(), req)
			if supported {
				if !errors.Is(err, devicegate.ErrAdmissionSyncUnsupported) || reply.Lease != "" || c.DeviceAdmissionSyncSupported() {
					t.Fatal("回滚后无状态确认仍接受授权")
				}
			} else if err != nil || reply.Status != "allowed" {
				t.Fatal(err)
			}
		})
	}
}
