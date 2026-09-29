package machine

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
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gorilla/websocket"
)

func TestMachineConnectionSynchronizesBeforeStoppingFallback(t *testing.T) {
	var fail atomic.Bool
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws" {
			upgrader := websocket.Upgrader{}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]string{"event": "auth.success"})
			_ = conn.WriteJSON(map[string]string{"event": "sync.ready"})
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}
		reads.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"nodes": []interface{}{}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	ws := panel.NewWSClient(strings.Replace(server.URL, "http", "ws", 1)+"/ws", "test-only", 0,
		panel.WSClientConfig{Realtime: true, MachineID: 7}, nil, nil, nil)
	done := make(chan struct{})
	go func() { ws.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for !ws.IsConnected() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !ws.IsConnected() {
		t.Fatal("测试长连接未完成同步")
	}
	o := &Orchestrator{client: panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 7}), ws: ws}
	if o.discoverySocket.Load() != nil {
		t.Fatal("只连上通道不能停止节点列表兜底")
	}
	fail.Store(true)
	o.rediscoverOnce(ctx)
	if o.discoverySocket.Load() != nil {
		t.Fatal("失败的同步不能停止兜底")
	}
	fail.Store(false)
	o.rediscoverOnce(ctx)
	if o.discoverySocket.Load() != ws || o.discoveryVersion.Load() != ws.Generation() {
		t.Fatal("完整同步后没有记录当前连接代次")
	}
	o.onWSStatus(panel.WSStatusChange{Connected: false})
	if o.discoverySocket.Load() != nil {
		t.Fatal("断线没有撤销已同步标记")
	}
	if reads.Load() != 2 {
		t.Fatalf("读取次数不符：%d", reads.Load())
	}
}
