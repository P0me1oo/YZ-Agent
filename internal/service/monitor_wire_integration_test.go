//go:build integration

package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gorilla/websocket"
)

// Read the actual WebSocket frames from TCP, including masking, headers and
// fragmentation. Exclude connection setup and the initial configuration sync.
type measuredConn struct {
	net.Conn
	read, written atomic.Int64
}

func (c *measuredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}
func (c *measuredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

type measuredListener struct{ net.Listener }

func (l *measuredListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &measuredConn{Conn: c}, nil
}

type stateExchangeSize struct{ send, receive int64 }

func TestMonitorStateWireSize(t *testing.T) {
	frames := make(chan stateExchangeSize, 4)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/handshake"):
			_, _ = w.Write([]byte(`{"host_monitoring":false,"realtime":{"version":1,"traffic_ack":true}}`))
		case strings.HasSuffix(r.URL.Path, "/begin"):
			_, _ = w.Write([]byte(`{"data":{"epoch":"bench-epoch"}}`))
		case r.URL.Path == "/ws":
			measureStateFrames(t, w, r, frames)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	server.Listener = &measuredListener{server.Listener}
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	ready := make(chan struct{}, 1)
	ws := panel.NewWSClient("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "", 1,
		panel.WSClientConfig{Realtime: true, MachineID: 1}, nil, func(st panel.WSStatusChange) {
			if st.Connected {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}, nil)
	done := make(chan struct{})
	go func() { defer close(done); ws.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("WebSocket did not connect")
	}
	client := panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1, NodeID: 1})
	if _, err := client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	client.SetWebSocketProvider(func() *panel.WSClient { return ws })
	cp := controlplane.NewMachinePanelControlPlane(client, nil, nil)
	s, _ := newShutdownReportService()
	s.source, s.sink = cp, cp
	s.operations.snapshot.Store(&operationsSnapshot{Run: "running", Version: 1})
	ips := map[int]map[string]bool{}
	counts := map[int]int{}
	ipCounts := map[int]map[string]int{}
	for i := 1; i <= 128; i++ {
		ips[i] = map[string]bool{"8.8.8.8": true}
		counts[i] = 2
		ipCounts[i] = map[string]int{"8.8.8.8": 2}
	}
	s.tracker.Process(nil, ips, 256)
	s.tracker.ProcessConnectionCounts(counts, nil)
	s.tracker.ProcessIPConnectionCounts(ipCounts)
	var sizes [4]stateExchangeSize
	for i := range sizes {
		s.publishRuntimeState(ctx)
		for s.stateActive.Load() && ctx.Err() == nil {
			runtime.Gosched()
		}
		select {
		case sizes[i] = <-frames:
		case <-ctx.Done():
			t.Fatal("state exchange timed out")
		}
	}
	full, summary := sizes[1], sizes[3]
	t.Logf("128 users, bytes sent/received per acknowledged state: full %d/%d, omit_device_details %d/%d", full.send, full.receive, summary.send, summary.receive)
	if summary.send+summary.receive >= full.send+full.receive {
		t.Fatal("negotiated omission did not reduce payload")
	}
}

func measureStateFrames(t *testing.T, w http.ResponseWriter, r *http.Request, frames chan<- stateExchangeSize) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	for _, event := range []string{"auth.success", "sync.ready"} {
		if err := conn.WriteJSON(map[string]string{"event": event}); err != nil {
			t.Error(err)
			return
		}
	}
	tcp := conn.UnderlyingConn().(*measuredConn)
	for i := 1; i <= 4; i++ {
		beforeRead, beforeWritten := tcp.read.Load(), tcp.written.Load()
		var msg struct {
			Data struct {
				panel.StateVersion
				Request string `json:"request_id"`
				Node    int    `json:"node_id"`
				State   map[string]json.RawMessage
			}
		}
		if err := conn.ReadJSON(&msg); err != nil {
			t.Error(err)
			return
		}
		if _, present := msg.Data.State["alive"]; present != (i < 4) {
			t.Error("unexpected device snapshot shape")
		}
		demand := panel.TelemetryDemand{DetailInterval: 60, LeaseSeconds: 60, OmitDeviceDetails: i >= 3}
		receipt := map[string]any{"accepted": true, "epoch": msg.Data.Epoch, "sequence": msg.Data.Sequence, "request_id": msg.Data.Request, "node_id": msg.Data.Node, "host_monitoring": false, "telemetry": demand}
		ack, err := json.Marshal(map[string]any{"event": "state.ack", "data": receipt})
		if err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, ack); err != nil {
			t.Error(err)
			return
		}
		frames <- stateExchangeSize{tcp.read.Load() - beforeRead, tcp.written.Load() - beforeWritten}
	}
	// Hold the connection through delivery of the last receipt.
	_, _, _ = conn.ReadMessage()
}
