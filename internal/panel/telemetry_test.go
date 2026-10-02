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

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestTelemetryDemandLeaseAndCompatibility(t *testing.T) {
	c := &Client{}
	check := func(details, speeds bool) {
		t.Helper()
		d, s := c.TelemetryNeeds()
		if d != details || s != speeds {
			t.Fatalf("采样需求 = %v/%v，预期 %v/%v", d, s, details, speeds)
		}
	}
	check(true, true)
	idle := &TelemetryDemand{DetailInterval: 60, LeaseSeconds: 30}
	c.acceptTelemetry(idle, StatePayload{})
	check(true, false)
	c.acceptTelemetry(idle, StatePayload{Status: map[string]interface{}{"cpu": 1}})
	check(false, false)
	c.realtime.lastDetail.Store(time.Now().Add(-61 * time.Second).UnixNano())
	check(true, false)
	c.acceptTelemetry(&TelemetryDemand{DetailInterval: 1, UserSpeeds: true, LeaseSeconds: 30}, StatePayload{Metrics: map[string]interface{}{}})
	check(true, true)
	c.acceptTelemetry(idle, StatePayload{Status: map[string]interface{}{}})
	c.realtime.telemetry.Store(&telemetryLease{demand: *idle, until: time.Now().Add(-time.Second)})
	check(true, true)
	c.acceptTelemetry(nil, StatePayload{})
	check(true, true)
	c.acceptTelemetry(&TelemetryDemand{DetailInterval: 999, LeaseSeconds: 30}, StatePayload{})
	check(true, true)
}

func TestTelemetryHTTPAcknowledgementControlsNextSample(t *testing.T) {
	var watching atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/begin") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": StateVersion{Epoch: strings.Repeat("a", 32)}})
			return
		}
		var body struct {
			StateVersion
			TelemetryMode int `json:"telemetry_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TelemetryMode != 1 {
			t.Error("没有声明按需采样能力")
		}
		interval := 60
		if watching.Load() {
			interval = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": RPCReceipt{StateVersion: body.StateVersion, Accepted: true,
			Telemetry: &TelemetryDemand{DetailInterval: interval, UserSpeeds: watching.Load(), LeaseSeconds: 30}}})
	}))
	defer server.Close()
	c := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	c.realtime.enabled.Store(true)
	if err := c.PublishState(context.Background(), StatePayload{Status: map[string]interface{}{"cpu": 1}}); err != nil {
		t.Fatal(err)
	}
	if d, s := c.TelemetryNeeds(); d || s {
		t.Fatal("空闲确认未停止高频展示")
	}
	watching.Store(true)
	c.realtime.lastFallback = time.Now().Add(-11 * time.Second)
	if err := c.PublishState(context.Background(), StatePayload{}); err != nil {
		t.Fatal(err)
	}
	if d, s := c.TelemetryNeeds(); !d || !s {
		t.Fatal("HTTP 订阅确认未恢复展示")
	}
}

func TestRejectedHeartbeatImmediatelyRestoresFullTelemetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	c := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	c.realtime.enabled.Store(true)
	c.realtime.version = StateVersion{Epoch: strings.Repeat("a", 32)}
	c.acceptTelemetry(&TelemetryDemand{DetailInterval: 60, LeaseSeconds: 30}, StatePayload{Status: map[string]interface{}{}})
	before := c.realtime.lastDetail.Load()
	if err := c.PublishState(context.Background(), StatePayload{}); err == nil {
		t.Fatal("错误确认被接受")
	}
	if details, speeds := c.TelemetryNeeds(); !details || !speeds {
		t.Fatal("面板拒绝后未立即恢复完整采样")
	}
	if c.realtime.lastDetail.Load() != before {
		t.Fatal("未确认的报告更新了采样时间")
	}
}

func TestTelemetryReceiptsAreIsolatedOnSharedMachineSocket(t *testing.T) {
	ws := NewWSClient("", "", 0, WSClientConfig{}, nil, nil, nil)
	ws.writeCh = make(chan wsMessage, 2)
	ws.connected.Store(true)
	clients := []*Client{NewClient(config.PanelConfig{NodeID: 1}), NewClient(config.PanelConfig{})}
	clients[1].machineID = 7
	for _, c := range clients {
		c.realtime.enabled.Store(true)
		c.realtime.version = StateVersion{Epoch: strings.Repeat("a", 32)}
		c.SetWebSocketProvider(func() *WSClient { return ws })
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, c := range clients {
		go func(c *Client) { done <- c.PublishState(ctx, StatePayload{Status: map[string]interface{}{"cpu": 1}}) }(c)
	}
	for range clients {
		var msg wsMessage
		select {
		case msg = <-ws.writeCh:
		case <-ctx.Done():
			t.Fatal("没有发送状态")
		}
		var receipt RPCReceipt
		if err := json.Unmarshal(msg.Data, &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.Accepted = true
		receipt.Telemetry = &TelemetryDemand{DetailInterval: 60, LeaseSeconds: 30}
		if receipt.NodeID == 0 {
			receipt.Telemetry.DetailInterval = 1
		}
		body, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		ws.receiveReceipt(wsMessage{Event: "state.ack", Data: body})
	}
	for range clients {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if details, _ := clients[0].TelemetryNeeds(); details {
		t.Fatal("机器订阅错误开启了节点的详细采样")
	}
	if details, _ := clients[1].TelemetryNeeds(); !details {
		t.Fatal("机器订阅确认没有生效")
	}
}

func TestIdleStateRetainsEmptyBusinessSnapshots(t *testing.T) {
	state := StatePayload{Alive: map[int][]string{}, Online: map[int]int{}, ConnectionCounts: map[int]int{}, RelayConnectionCounts: map[int]map[int]int{}}
	data := state.mapValue()
	for _, key := range []string{"alive", "online", "connection_counts", "relay_connection_counts"} {
		if _, ok := data[key]; !ok {
			t.Fatalf("必要的空快照被省略：%s", key)
		}
	}
	for _, key := range []string{"status", "metrics", "user_speeds"} {
		if _, ok := data[key]; ok {
			t.Fatalf("无人查看时仍发送展示数据：%s", key)
		}
	}
}
