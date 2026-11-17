package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestAccessLogGenerationRejectsStaleBatchAndSharesMachinePolicy(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["generation"] != strings.Repeat("b", 32) {
			t.Error("上报没有保留批次的停用代次")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": body["batch_id"]}})
	}))
	defer server.Close()
	parent := NewClient(config.PanelConfig{URL: server.URL, MachineID: 3})
	child := parent.ForNode(7)
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	parent.applyAccessLog(&AccessLogPolicy{Version: 2, Generation: &first})
	_, before := child.AccessLogState()
	// 断连或两次回复之间发生了完整关闭再开启，当前仍是开启状态。
	parent.applyAccessLog(&AccessLogPolicy{Version: 2, Generation: &second})
	_, after := child.AccessLogState()
	if after == before || child.AccessLogGeneration() != second {
		t.Fatal("相同开关状态下漏掉了停用代次变化")
	}
	child.applyAccessLog(&AccessLogPolicy{Version: 2, Generation: &first})
	parent.applyAccessLog(&AccessLogPolicy{Version: 2})
	if child.AccessLogGeneration() != second {
		t.Fatal("迟到节点或缺少字段的回复覆盖了机器停用代次")
	}
	batch := &accesslog.Batch{ID: strings.Repeat("c", 32), Generation: first}
	if err := child.ReportAccessLog(t.Context(), batch); err == nil || posts.Load() != 0 {
		t.Fatal("旧批次仍发出了网络请求")
	}
	batch.Generation = second
	if err := child.ReportAccessLog(t.Context(), batch); err != nil || posts.Load() != 1 {
		t.Fatal("当前代次的批次无法上报", err)
	}
}

func TestAccessLogDynamicPolicyAndMachineSharing(t *testing.T) {
	for _, endpoint := range []string{"handshake", "nodes", "control"} {
		t.Run(endpoint, func(t *testing.T) {
			var mode atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode.Load() == 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				body := map[string]any{}
				if mode.Load() != 3 {
					body["access_log"] = map[string]any{"version": 2, "enabled": mode.Load() == 1}
				} else if endpoint == "handshake" {
					body["access_log"] = map[string]any{"version": 2}
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 3})
			child := client.ForNode(7)
			call := func() error {
				switch endpoint {
				case "nodes":
					_, err := client.GetMachineNodes()
					return err
				case "control":
					_, err := client.ExchangeMachineControl("test", "test-run", false, nil)
					return err
				default:
					_, err := client.Handshake()
					return err
				}
			}
			for _, step := range []int32{1, 0, 2, 3, 1} {
				mode.Store(step)
				if err := call(); (err != nil) != (step == 2) {
					t.Fatalf("接口响应错误不符：%v", err)
				}
				want := step == 1 || (step == 3 && endpoint == "handshake")
				if client.AccessLogEnabled() != want || child.AccessLogEnabled() != want {
					t.Fatalf("机器和节点未同步访问策略，阶段 %d", step)
				}
				value := !want
				child.applyAccessLog(&AccessLogPolicy{Version: 2, Enabled: &value})
				if child.AccessLogEnabled() != want {
					t.Fatal("迟到节点响应覆盖了机器策略")
				}
			}
			if _, revision := child.AccessLogState(); revision == 0 {
				t.Fatal("重新开启丢失了停用标识，可能补报旧队列")
			}
		})
	}
}

func TestAccessLogPolicyViaRuntimeReceipts(t *testing.T) {
	for _, identity := range []string{"node", "machine"} {
		for _, transport := range []string{"http", "websocket"} {
			t.Run(identity+"/"+transport, func(t *testing.T) {
				var enabled atomic.Bool
				var mode atomic.Int32
				receipt := func(body map[string]any) RPCReceipt {
					value := enabled.Load()
					result := RPCReceipt{StateVersion: StateVersion{Epoch: body["epoch"].(string), Sequence: uint64(body["sequence"].(float64))},
						RequestID: body["request_id"].(string), NodeID: int(body["node_id"].(float64)), Accepted: true}
					if mode.Load() == 0 {
						result.AccessLog = &AccessLogPolicy{Version: 2, Enabled: &value}
					}
					return result
				}
				cfg := config.PanelConfig{MachineID: 3}
				if identity == "node" {
					cfg.NodeID = 7
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					body["request_id"] = ""
					body["node_id"] = float64(cfg.NodeID)
					_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt(body)})
				}))
				defer server.Close()
				cfg.URL = server.URL
				c := NewClient(cfg)
				c.applyAccessLog(&AccessLogPolicy{Version: 2})
				c.realtime.enabled.Store(true)
				c.realtime.version = StateVersion{Epoch: "access-policy-test"}
				ws := NewWSClient("", "", 0, WSClientConfig{}, nil, nil, nil)
				if transport == "websocket" {
					ws.writeCh = make(chan wsMessage, 1)
					ws.connected.Store(true)
					c.SetWebSocketProvider(func() *WSClient { return ws })
				}
				for index, step := range []bool{false, true, false, false} {
					if index == 3 {
						mode.Store(1)
					}
					enabled.Store(step)
					c.realtime.lastFallback = time.Time{}
					done := make(chan error, 1)
					go func() { done <- c.PublishState(context.Background(), StatePayload{}) }()
					if transport == "websocket" {
						select {
						case msg := <-ws.writeCh:
							var body map[string]any
							_ = json.Unmarshal(msg.Data, &body)
							data, _ := json.Marshal(receipt(body))
							ws.receiveReceipt(wsMessage{Event: "state.ack", Data: data})
						case <-time.After(time.Second):
							t.Fatal("未发送状态心跳")
						}
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					if c.AccessLogEnabled() != step {
						t.Fatal("保持连接时未更新访问记录开关")
					}
				}
			})
		}
	}
}

func TestAccessLogProtocolAuthCapabilityAndReceipt(t *testing.T) {
	mode := "enabled"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if data["machine_id"] != float64(3) || data["node_id"] != float64(7) || data["token"] != "access-test-only" {
			t.Error("请求认证身份错误")
		}
		if r.URL.Path == "/api/v2/server/handshake" {
			version := 2
			if mode == "legacy" {
				version = 0
			}
			if mode == "entry-only" {
				version = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_log": map[string]any{"version": version, "enabled": mode != "disabled"}})
			return
		}
		if r.URL.Path != "/api/v2/server/access-log" {
			t.Error("上报路径错误")
		}
		records := data["records"].([]any)
		if records[0].(map[string]any)["node_id"] != float64(9) {
			t.Error("实际落地归属丢失或覆盖了入口认证")
		}
		if mode == "fail" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("不要回显访问详情"))
			return
		}
		id := data["batch_id"]
		if mode == "wrong" {
			id = "wrong"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": id}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 3, Token: "access-test-only"}).ForNode(7)
	batch := &accesslog.Batch{ID: strings.Repeat("a", 32), Records: []accesslog.Record{{NodeID: 9, UserID: 11, Destination: "example.test", Port: 443, ConnectedAt: time.Now().Unix()}}}
	if err := client.ReportAccessLog(t.Context(), batch); err == nil {
		t.Fatal("握手前不得上报")
	}
	if _, err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := client.ReportAccessLog(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	mode = "wrong"
	if err := client.ReportAccessLog(t.Context(), batch); err == nil {
		t.Fatal("错误批次被确认")
	}
	mode = "fail"
	if err := client.ReportAccessLog(t.Context(), batch); err == nil || strings.Contains(err.Error(), "详情") {
		t.Fatal("失败响应处理错误")
	}
	mode = "disabled"
	if _, err := client.Handshake(); err != nil || client.AccessLogEnabled() {
		t.Fatal("关闭代理访问记录后仍允许上报")
	}
	if err := client.ReportAccessLog(t.Context(), batch); err == nil {
		t.Fatal("关闭后仍上报访问记录")
	}
	mode = "enabled"
	if _, err := client.Handshake(); err != nil || !client.AccessLogEnabled() {
		t.Fatal("重新开启访问记录失败")
	}
	mode = "legacy"
	if _, err := client.Handshake(); err != nil || client.AccessLogEnabled() {
		t.Fatal("旧面板能力回退失败")
	}
	mode = "entry-only"
	if _, err := client.Handshake(); err != nil || client.AccessLogEnabled() {
		t.Fatal("只支持入口归属的旧面板不得启用新版采集")
	}
}
