package machine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestEmptyMachineKeepsRealtimeHeartbeatAndResumesLegacyLoad(t *testing.T) {
	var enabled atomic.Bool
	var loads atomic.Int32
	states := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/server/machine/nodes", "/api/v2/server/handshake":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"host_monitoring": enabled.Load(), "realtime": map[string]interface{}{"version": 1, "traffic_ack": true}})
		case "/api/v2/server/machine/status":
			loads.Add(1)
			_, _ = w.Write([]byte(`{"data":true}`))
		case "/api/v2/server/machine/realtime/begin":
			_, _ = w.Write([]byte(`{"data":{"epoch":"machine-test"}}`))
		case "/api/v2/server/machine/realtime/state":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			states <- body["state"].(map[string]interface{})
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{"epoch": body["epoch"], "sequence": body["sequence"], "host_monitoring": enabled.Load()}})
		default:
			t.Errorf("意外请求 %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 7})
	if _, err := c.Handshake(); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{client: c}
	o.reportMachineStatus()
	if loads.Load() != 0 {
		t.Fatal("Lite 模式仍上报机器负载")
	}
	o.publishRuntimeState(context.Background())
	select {
	case state := <-states:
		if len(state) != 0 {
			t.Fatal("空机器心跳仍带主机指标")
		}
	case <-time.After(time.Second):
		t.Fatal("空机器没有心跳")
	}
	deadline := time.Now().Add(time.Second)
	for o.stateActive.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if o.stateActive.Load() {
		t.Fatal("心跳没有完成")
	}
	enabled.Store(true)
	if _, err := c.GetMachineNodes(); err != nil {
		t.Fatal(err)
	}
	o.reportMachineStatus()
	if loads.Load() != 1 {
		t.Fatal("关闭 Lite 后没有恢复机器负载")
	}
}
