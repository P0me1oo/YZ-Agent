package panel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

func TestDeviceHandoverHTTPPreservesMachineNodeAuthAndEmptySnapshot(t *testing.T) {
	run, lease := strings.Repeat("a", 32), strings.Repeat("b", 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("设备接口必须使用 POST")
		}
		var data map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if string(data["machine_id"]) != "3" || string(data["node_id"]) != "7" || string(data["token"]) != `"handover-test-only"` {
			t.Error("设备请求没有保留机器与节点鉴权")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/server/handshake":
			_, _ = w.Write([]byte(`{"realtime":{"version":1,"traffic_ack":true,"device_handover":1}}`))
		case "/api/v2/server/device-handover/begin":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.BeginReply{Version: 1, Run: run}})
		case "/api/v2/server/device-handover/admit":
			if string(data["user_id"]) != "10" || string(data["sequence"]) != "1" {
				t.Error("准入账号或序号编码错误")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.AdmissionReply{Status: "allowed", Lease: lease}})
		case "/api/v2/server/device-handover/sync":
			if string(data["sources"]) != "[]" || string(data["pending"]) != "[]" {
				t.Error("空快照不能编码成缺失或 null")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": devicegate.SyncReply{Run: run, Sequence: 2}})
		default:
			t.Error("请求了错误的设备接口路径")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, Token: "handover-test-only", MachineID: 3}).ForNode(7)
	if _, err := client.Handshake(); err != nil || !client.DeviceHandoverSupported() {
		t.Fatalf("能力握手失败：%v", err)
	}
	if reply, err := client.BeginDeviceSession(t.Context(), run); err != nil || reply.Run != run {
		t.Fatalf("设备会话建立失败：%v", err)
	}
	if reply, err := client.AdmitDeviceSource(t.Context(), devicegate.AdmissionRequest{Run: run, Sequence: 1, UserID: 10, IP: "8.8.8.8"}); err != nil || reply.Lease != lease {
		t.Fatalf("来源准入失败：%v", err)
	}
	if reply, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Run: run, Sequence: 2, Sources: []devicegate.Source{}, Pending: []uint64{}}); err != nil || reply.Sequence != 2 {
		t.Fatalf("空快照确认失败：%v", err)
	}
}

func TestDeviceHandoverHTTPConflictIsNotTreatedAsAnAdmission(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, Token: "handover-test-only", NodeID: 7})
	_, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Sources: []devicegate.Source{}, Pending: []uint64{}})
	if !errors.Is(err, devicegate.ErrSessionLost) {
		t.Fatalf("会话冲突未要求重新确认来源：%v", err)
	}
}
