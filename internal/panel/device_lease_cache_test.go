package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

func TestDeviceLeaseCacheHTTPNegotiatesAndPreservesExpiryAndRetirement(t *testing.T) {
	run, lease := strings.Repeat("a", 32), strings.Repeat("b", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/server/handshake" {
			_, _ = w.Write([]byte(`{"realtime":{"version":1,"device_handover":1,"device_lease_cache":1}}`))
			return
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if string(payload["lease_cache"]) != "1" {
			t.Error("未携带许可缓存协商标记")
		}
		switch r.URL.Path {
		case "/api/v2/server/device-handover/begin":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.BeginReply{Run: run, Version: 1, LeaseCache: 1}})
		case "/api/v2/server/device-handover/admit":
			if len(payload["snapshot"]) == 0 {
				t.Error("许可申请缺少真实状态")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.AdmissionReply{Status: "allowed", Lease: lease,
				ConfirmedAt: 1000, ExpiresAt: 3601000, ServerTime: 1100, Sync: &devicegate.SyncReply{Run: run, Sequence: 2}}})
		case "/api/v2/server/device-handover/sync":
			var retired []devicegate.Revocation
			if err := json.Unmarshal(payload["retired"], &retired); err != nil || len(retired) != 1 || retired[0].Lease != lease {
				t.Error("真实关闭标记编码错误")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.SyncReply{Run: run, Sequence: 3, LeaseCache: 1,
				ServerTime: 1100, Leases: []devicegate.Lease{{UserID: 1, IP: "8.8.8.8", Lease: lease, ConfirmedAt: 1000, ExpiresAt: 3601000}}}})
		default:
			t.Error("错误的接口路径")
		}
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, Token: "lease-cache-test-only", NodeID: 7})
	if _, err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if reply, err := client.BeginDeviceSession(t.Context(), run); err != nil || reply.LeaseCache != 1 {
		t.Fatalf("握手失败：%v", err)
	}
	reply, err := client.AdmitDeviceSource(t.Context(), devicegate.AdmissionRequest{Run: run, Sequence: 1, UserID: 1,
		IP: "8.8.8.8", LeaseCache: 1, Snapshot: &devicegate.Snapshot{Run: run, Sequence: 2, Pending: []uint64{1}, Sources: []devicegate.Source{}}})
	if err != nil || reply.ConfirmedAt != 1000 || reply.ExpiresAt != 3601000 || reply.ServerTime != 1100 {
		t.Fatalf("期限传输错误：%v", err)
	}
	synced, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Run: run, Sequence: 3, LeaseCache: 1,
		Pending: []uint64{}, Sources: []devicegate.Source{}, Retired: []devicegate.Revocation{{UserID: 1, IP: "8.8.8.8", Lease: lease}}})
	if err != nil || synced.LeaseCache != 1 || len(synced.Leases) != 1 || synced.Leases[0].ExpiresAt != 3601000 {
		t.Fatalf("同步许可编码错误：%v", err)
	}
}
