package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
	"github.com/gorilla/websocket"
)

func deviceTestSocket(t *testing.T, serverURL string) *WSClient {
	t.Helper()
	ready := make(chan struct{}, 1)
	ws := NewWSClient("ws"+strings.TrimPrefix(serverURL, "http")+"/ws", "device-ws-test-only", 7,
		WSClientConfig{Realtime: true}, nil, func(status WSStatusChange) {
			if status.Connected {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); ws.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("长连接测试未退出")
		}
	})
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("长连接未就绪")
	}
	return ws
}

func TestDeviceHandoverWSUsesRPCAndFallsBackWithSameAdmissionAfterLostAck(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		name := "确认成功"
		if lostAck {
			name = "确认丢失"
		}
		t.Run(name, func(t *testing.T) {
			var httpCalls atomic.Int32
			var mu sync.Mutex
			var admitted map[string]interface{}
			var wsEvents []string
			run, lease := strings.Repeat("a", 32), strings.Repeat("b", 32)
			upgrader := websocket.Upgrader{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ws" {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.WriteJSON(wsMessage{Event: "auth.success"})
					_ = conn.WriteJSON(wsMessage{Event: "sync.ready"})
					for {
						var msg wsMessage
						if conn.ReadJSON(&msg) != nil {
							return
						}
						var payload map[string]interface{}
						if json.Unmarshal(msg.Data, &payload) != nil {
							return
						}
						mu.Lock()
						wsEvents = append(wsEvents, msg.Event)
						mu.Unlock()
						var result interface{}
						switch msg.Event {
						case "device.begin":
							result = devicegate.BeginReply{Run: run, Version: 1}
						case "device.sync":
							result = devicegate.SyncReply{Run: run, Sequence: uint64(payload["sequence"].(float64))}
						case "device.admit":
							mu.Lock()
							admitted = map[string]interface{}{"run": payload["run"], "sequence": payload["sequence"], "user_id": payload["user_id"], "ip": payload["ip"]}
							mu.Unlock()
							if lostAck {
								return
							}
							result = devicegate.AdmissionReply{Status: "allowed", Lease: lease}
						default:
							t.Errorf("意外事件：%s", msg.Event)
							return
						}
						body, _ := json.Marshal(result)
						data, _ := json.Marshal(RPCReceipt{RequestID: payload["request_id"].(string), NodeID: 7, Accepted: true, Result: body})
						if conn.WriteJSON(wsMessage{Event: msg.Event + ".ack", Data: data}) != nil {
							return
						}
					}
				}
				httpCalls.Add(1)
				var payload map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if !lostAck || r.URL.Path != "/api/v2/server/device-handover/admit" {
					t.Error("不应回退到该 HTTP 接口")
				}
				if payload["token"] != "device-http-test-only" || payload["node_id"] != float64(7) {
					t.Error("HTTP 补偿遗漏鉴权")
				}
				delete(payload, "token")
				delete(payload, "node_id")
				mu.Lock()
				if !reflect.DeepEqual(admitted, payload) {
					t.Error("通道切换改变了原申请，可能重复授予来源")
				}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": devicegate.AdmissionReply{Status: "allowed", Lease: lease}})
			}))
			t.Cleanup(server.Close)
			ws := deviceTestSocket(t, server.URL)
			client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7, Token: "device-http-test-only"})
			client.deviceHandoverWS.Store(true)
			client.SetWebSocketProvider(func() *WSClient { return ws })
			if reply, err := client.BeginDeviceSession(t.Context(), run); err != nil || reply.Run != run {
				t.Fatalf("建立会话失败：%v", err)
			}
			if reply, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Run: run, Sequence: 1, Pending: []uint64{}, Sources: []devicegate.Source{}}); err != nil || reply.Sequence != 1 {
				t.Fatalf("完整同步失败：%v", err)
			}
			if reply, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Run: run, Sequence: 2, Unchanged: true, BaseSequence: 1}); err != nil || reply.Sequence != 2 {
				t.Fatalf("续期失败：%v", err)
			}
			if reply, err := client.AdmitDeviceSource(t.Context(), devicegate.AdmissionRequest{Run: run, Sequence: 3, UserID: 10, IP: "8.8.8.8"}); err != nil || reply.Lease != lease {
				t.Fatalf("准入失败：%v", err)
			}
			wantHTTP := int32(0)
			if lostAck {
				wantHTTP = 1
			}
			if httpCalls.Load() != wantHTTP {
				t.Errorf("HTTP 调用数：%d，期望 %d", httpCalls.Load(), wantHTTP)
			}
			mu.Lock()
			if !reflect.DeepEqual(wsEvents, []string{"device.begin", "device.sync", "device.sync", "device.admit"}) {
				t.Error("设备请求未全部复用长连接")
			}
			mu.Unlock()
		})
	}
}

func TestDeviceRenewalNegotiationAndRollbackUsesFullSnapshotFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/server/handshake" {
			_, _ = w.Write([]byte(`{"realtime":{"device_handover":1,"device_handover_ws":1,"device_handover_renewal":1}}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7})
	if _, err := client.Handshake(); err != nil || !client.DeviceRenewalSupported() || !client.deviceHandoverWS.Load() {
		t.Fatalf("新能力未协商：%v", err)
	}
	_, err := client.SyncDeviceSession(t.Context(), devicegate.Snapshot{Run: strings.Repeat("a", 32), Sequence: 2, Unchanged: true, BaseSequence: 1})
	if !errors.Is(err, devicegate.ErrSnapshotRequired) || client.DeviceRenewalSupported() {
		t.Fatal("面板回滚后应停用续期并补发完整快照")
	}
}

func TestDeviceRPCReceiptMustMatchNodeRequestAndOperation(t *testing.T) {
	ws := NewWSClient("", "", 7, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 1)
	ws.connected.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := ws.Request(ctx, "device.sync", 7, map[string]interface{}{}); done <- err }()
	msg := <-ws.writeCh
	var body struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(msg.Data, &body)
	for _, receipt := range []struct {
		event, id string
		node      int
	}{
		{"device.sync.ack", body.RequestID, 8}, {"device.admit.ack", body.RequestID, 7}, {"device.sync.ack", "retired-request", 7},
	} {
		data, _ := json.Marshal(RPCReceipt{RequestID: receipt.id, NodeID: receipt.node, Accepted: true})
		ws.receiveReceipt(wsMessage{Event: receipt.event, Data: data})
		select {
		case <-done:
			t.Fatal("其他节点、请求或操作的确认被错误接受")
		default:
		}
	}
	data, _ := json.Marshal(RPCReceipt{RequestID: body.RequestID, NodeID: 7, Code: 409})
	ws.receiveReceipt(wsMessage{Event: "device.sync.error", Data: data})
	var remote *RemoteError
	if err := <-done; !errors.As(err, &remote) || remote.Code != 409 {
		t.Fatalf("对应错误未唤醒原请求：%v", err)
	}
}

func TestMalformedDeviceReceiptCannotContaminateHTTPFallback(t *testing.T) {
	for _, body := range []string{`{"data":null}`, `{"data":{}}`, `{"data":{"status":"denied"}}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			ws := NewWSClient("", "", 7, WSClientConfig{}, nil, nil, nil)
			ws.writeCh = make(chan wsMessage, 1)
			ws.connected.Store(true)
			client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 7})
			client.deviceHandoverWS.Store(true)
			client.SetWebSocketProvider(func() *WSClient { return ws })
			done := make(chan struct{})
			go func() {
				defer close(done)
				reply, err := client.AdmitDeviceSource(t.Context(), devicegate.AdmissionRequest{Run: strings.Repeat("a", 32), Sequence: 1, UserID: 1, IP: "8.8.8.8"})
				if reply.Status == "allowed" || reply.Lease != "" || (body == `{"data":null}` && err == nil) {
					t.Error("损坏的长连接响应污染了 HTTP 补偿结果")
				}
			}()
			msg := <-ws.writeCh
			var payload struct {
				RequestID string `json:"request_id"`
			}
			_ = json.Unmarshal(msg.Data, &payload)
			data, _ := json.Marshal(RPCReceipt{RequestID: payload.RequestID, NodeID: 7, Accepted: true,
				Result: json.RawMessage(`{"status":"allowed","lease":"` + strings.Repeat("b", 32) + `","observed":"invalid"}`)})
			ws.receiveReceipt(wsMessage{Event: "device.admit.ack", Data: data})
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("损坏响应未转入补偿")
			}
		})
	}
}
