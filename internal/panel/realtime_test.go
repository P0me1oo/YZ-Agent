package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/gorilla/websocket"
)

func TestRealtimeHTTPPreservesEmptySnapshotsAndUsesFallbackInterval(t *testing.T) {
	var posts atomic.Int32
	var sequence atomic.Uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/api/v2/server/realtime/begin":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": StateVersion{Epoch: strings.Repeat("a", 32)}})
		case "/api/v2/server/realtime/state":
			posts.Add(1)
			state, ok := body["state"].(map[string]interface{})
			if !ok {
				t.Error("没有状态对象")
				w.WriteHeader(400)
				return
			}
			if _, ok := state["connection_counts"]; !ok {
				t.Error("空连接快照被省略")
			}
			if _, ok := state["alive"]; !ok {
				t.Error("空设备快照被省略")
			}
			seq := uint64(body["sequence"].(float64))
			sequence.Store(seq)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": RPCReceipt{StateVersion: StateVersion{Epoch: body["epoch"].(string), Sequence: seq}, Accepted: true}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	client.realtime.enabled.Store(true)
	state := StatePayload{Alive: map[int][]string{}, ConnectionCounts: map[int]int{}}
	if err := client.PublishState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := client.PublishState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("HTTP 兜底未遵循十秒间隔")
	}
	client.realtime.lastFallback = time.Now().Add(-11 * time.Second)
	if err := client.PublishState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || sequence.Load() != 2 {
		t.Fatal("下一次 HTTP 状态没有使用新序号")
	}
}

func TestRealtimeTrafficFallsBackWithUnchangedBatchAfterSocketCloses(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	settlements := 0
	var httpPosts atomic.Int32
	upgrader := websocket.Upgrader{}
	accept := func(id string) {
		mu.Lock()
		defer mu.Unlock()
		if !seen[id] {
			seen[id] = true
			settlements++
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(wsMessage{Event: "auth.success"})
			_ = conn.WriteJSON(wsMessage{Event: "sync.ready"})
			var message wsMessage
			if conn.ReadJSON(&message) != nil {
				return
			}
			var body map[string]interface{}
			_ = json.Unmarshal(message.Data, &body)
			accept(body["report_id"].(string))
			return // 接收后断开，不发送确认。
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/api/v2/server/report" {
			w.WriteHeader(404)
			return
		}
		httpPosts.Add(1)
		id, _ := body["report_id"].(string)
		if id != "test-persisted-batch" {
			t.Errorf("切换通道改变了批次编号: %q", id)
		}
		if _, ok := body["alive"]; ok {
			t.Error("流量重试携带了旧在线状态")
		}
		if _, ok := body["status"]; ok {
			t.Error("流量重试携带了旧负载状态")
		}
		accept(id)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": true, "receipt": RPCReceipt{ReportID: id, Accepted: true}})
	}))
	defer server.Close()
	ready := make(chan struct{}, 1)
	ws := NewWSClient("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "dedicated-test", 1, WSClientConfig{Realtime: true}, nil, func(status WSStatusChange) {
		if status.Connected {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); ws.Run(ctx) }()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("测试长连接未就绪")
	}
	client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	client.realtime.enabled.Store(true)
	client.SetWebSocketProvider(func() *WSClient { return ws })
	err := client.reportRealtime(ctx, map[string]interface{}{
		"report_id": "test-persisted-batch", "traffic": map[int][2]int64{1: {10, 20}},
		"alive": map[int][]string{1: {"8.8.8.8"}}, "status": map[string]interface{}{"cpu": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if httpPosts.Load() != 1 {
		t.Fatal("没有回退到 HTTP")
	}
	mu.Lock()
	count := settlements
	mu.Unlock()
	if count != 1 {
		t.Fatalf("同批次被模拟服务结算 %d 次", count)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("断开后长连接任务未退出")
	}
}

func TestRealtimeTrafficRequiresMatchingHTTPReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": true, "receipt": RPCReceipt{ReportID: "another-batch", Accepted: true}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	client.realtime.enabled.Store(true)
	if err := client.reportRealtime(context.Background(), map[string]interface{}{"report_id": "expected-batch"}); err == nil {
		t.Fatal("错误批次确认不能清理待重试数据")
	}
}

func TestWSLateReceiptCannotCompleteNextRequest(t *testing.T) {
	ws := NewWSClient("", "", 1, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 2)
	ws.connected.Store(true)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := ws.Request(firstCtx, "report.traffic", 1, map[string]interface{}{"report_id": "first"})
		firstDone <- err
	}()
	first := <-ws.writeCh
	var firstBody map[string]interface{}
	_ = json.Unmarshal(first.Data, &firstBody)
	cancelFirst()
	if <-firstDone == nil {
		t.Fatal("取消的请求应保留未确认状态")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := ws.Request(ctx, "report.traffic", 1, map[string]interface{}{"report_id": "second"})
		secondDone <- err
	}()
	second := <-ws.writeCh
	var secondBody map[string]interface{}
	_ = json.Unmarshal(second.Data, &secondBody)
	late, _ := json.Marshal(RPCReceipt{RequestID: firstBody["request_id"].(string), NodeID: 1, ReportID: "first", Accepted: true})
	ws.receiveReceipt(wsMessage{Event: "traffic.ack", Data: late})
	select {
	case <-secondDone:
		t.Fatal("迟到确认完成了下一次请求")
	default:
	}
	correct, _ := json.Marshal(RPCReceipt{RequestID: secondBody["request_id"].(string), NodeID: 1, ReportID: "second", Accepted: true})
	ws.receiveReceipt(wsMessage{Event: "traffic.ack", Data: correct})
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestVersionGateRejectsOlderAndRetiredBaselines(t *testing.T) {
	gate := VersionGate{}
	for _, version := range []StateVersion{{Epoch: "a", Sequence: 5}, {Epoch: "a", Sequence: 6}, {Epoch: "b", Sequence: 1}} {
		if !gate.Accept(version) {
			t.Fatal("新版本被拒绝")
		}
	}
	for _, version := range []StateVersion{{Epoch: "a", Sequence: 99}, {Epoch: "b", Sequence: 0}, {}} {
		if gate.Accept(version) {
			t.Fatal("旧版本重新生效")
		}
	}
}
