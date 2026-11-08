package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestHostMonitoringPolicyFromControlDiscoveryAndHandshake(t *testing.T) {
	for _, endpoint := range []string{"control", "nodes", "handshake"} {
		t.Run(endpoint, func(t *testing.T) {
			var mode atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode.Load() == 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				body := map[string]interface{}{}
				if mode.Load() != 3 {
					body["host_monitoring"] = mode.Load() == 1
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 7})
			child := client.ForNode(1)
			call := func() error {
				switch endpoint {
				case "control":
					_, err := client.ExchangeMachineControl("test", "test-run", false, nil)
					return err
				case "nodes":
					_, err := client.GetMachineNodes()
					return err
				default:
					_, err := client.Handshake()
					return err
				}
			}
			if !client.HostMonitoringEnabled() || !child.HostMonitoringEnabled() {
				t.Fatal("旧面板默认应继续监控")
			}
			for _, value := range []int32{0, 2, 1, 0, 3} {
				mode.Store(value)
				err := call()
				if (err != nil) != (value == 2) {
					t.Fatalf("响应错误不符：%v", err)
				}
				want := value == 1 || value == 3
				if client.HostMonitoringEnabled() != want || child.HostMonitoringEnabled() != want {
					t.Fatalf("机器和节点未同步策略，阶段 %d", value)
				}
				child.applyHostMonitoring(nil)
				if child.HostMonitoringEnabled() != want {
					t.Fatal("节点旧响应覆盖了机器策略")
				}
			}
		})
	}
}

func TestHostMonitoringFiltersRetriedReportWithoutLosingProxyData(t *testing.T) {
	var bodies []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"data":true}`))
	}))
	defer server.Close()
	c := NewClient(config.PanelConfig{URL: server.URL, MachineID: 7}).ForNode(1)
	enabled := false
	c.hostMonitoringParent.applyHostMonitoring(&enabled)
	metrics := map[string]interface{}{"cpu_per_core": []float64{50}, "load": map[string]int{"load1": 2}, "active_connections": 3, "kernel_status": true}
	if err := c.ReportContext(context.Background(), "same-batch", map[int][2]int64{1: {10, 20}}, nil, nil, nil,
		map[int][]string{1: {"8.8.8.8"}}, map[int]int{1: 1}, 20, [2]uint64{100, 10}, [2]uint64{}, [2]uint64{}, metrics, nil,
		ConnectionSnapshot{Users: map[int]int{1: 3}, IPs: map[int]map[string]int{1: {"8.8.8.8": 3}}}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || bodies[0]["status"] != nil || bodies[0]["report_id"] != "same-batch" {
		t.Fatal("旧批次仍携带负载或改变了批次编号")
	}
	for _, key := range []string{"traffic", "alive", "online", "connection_counts", "ip_connection_counts", "metrics"} {
		if bodies[0][key] == nil {
			t.Fatalf("丢失代理字段 %s", key)
		}
	}
	m := bodies[0]["metrics"].(map[string]interface{})
	if m["load"] != nil || m["cpu_per_core"] != nil || m["kernel_status"] != true || m["active_connections"] != float64(3) || metrics["load"] == nil {
		t.Fatal("过滤破坏代理指标或修改了原批次")
	}
}

func TestHostMonitoringRealtimeReceiptsAndRecovery(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			var enabled atomic.Bool
			var states []map[string]interface{}
			handle := func(body map[string]interface{}) RPCReceipt {
				states = append(states, body["state"].(map[string]interface{}))
				value := enabled.Load()
				return RPCReceipt{StateVersion: StateVersion{Epoch: body["epoch"].(string), Sequence: uint64(body["sequence"].(float64))},
					RequestID: body["request_id"].(string), Accepted: true, HostMonitoring: &value}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&body)
				body["request_id"] = ""
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": handle(body)})
			}))
			defer server.Close()
			c := NewClient(config.PanelConfig{URL: server.URL, MachineID: 7})
			c.realtime.enabled.Store(true)
			c.realtime.version = StateVersion{Epoch: "test-host-policy"}
			ws := NewWSClient("", "", 0, WSClientConfig{}, nil, nil, nil)
			if transport == "websocket" {
				ws.writeCh = make(chan wsMessage, 1)
				ws.connected.Store(true)
				c.SetWebSocketProvider(func() *WSClient { return ws })
			}
			for i := range 4 {
				if i == 2 {
					enabled.Store(true)
				}
				c.realtime.lastFallback = time.Time{}
				done := make(chan error, 1)
				go func() {
					done <- c.PublishState(context.Background(), StatePayload{Status: map[string]interface{}{"cpu": 12, "mem": map[string]int{"total": 100, "used": 10}}})
				}()
				if transport == "websocket" {
					select {
					case msg := <-ws.writeCh:
						var body map[string]interface{}
						_ = json.Unmarshal(msg.Data, &body)
						data, _ := json.Marshal(handle(body))
						ws.receiveReceipt(wsMessage{Event: "state.ack", Data: data})
					case <-time.After(time.Second):
						t.Fatal("未发送控制心跳")
					}
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if c.HostMonitoringEnabled() != (i >= 2) {
					t.Fatal("未应用回执策略")
				}
				if (states[i]["status"] != nil) != (i == 0 || i == 3) {
					t.Fatal("负载没有停报或恢复")
				}
			}
		})
	}
}
