//go:build linux && integration

package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gofrs/uuid/v5"
	"github.com/gorilla/websocket"
)

// Exercise production kernel snapshot readers with 128 open VLESS streams (one public
// source per account), and 896 further configured users. Payload stays on loopback.
func BenchmarkUserUsageSampling(b *testing.B) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, viewers := range []int{0, 1, 64} {
			b.Run(fmt.Sprintf("%s/viewed-%d", kind, viewers), func(b *testing.B) {
				service := usageBenchmarkNode(b, kind)
				frames := usageBenchmarkPanel(b, service, viewers)
				ctx := b.Context()
				tick := func() {
					b.Helper()
					if _, _, err := service.collectTraffic(ctx); err != nil {
						b.Fatal(err)
					}
				}
				publish := func() stateExchangeSize {
					b.Helper()
					service.publishRuntimeState(ctx)
					deadline := time.Now().Add(5 * time.Second)
					for service.stateActive.Load() && time.Now().Before(deadline) {
						runtime.Gosched()
					}
					select {
					case size := <-frames:
						return size
					case <-time.After(time.Second):
						b.Fatal("state acknowledgement missing")
						return stateExchangeSize{}
					}
				}
				// Complete epoch negotiation, the first detailed host report and the
				// first speed baseline outside the measurement interval.
				for range 3 {
					tick()
					publish()
				}
				tick()
				size := publish()
				b.ReportAllocs()
				b.ResetTimer()
				before := usageCPU(b)
				for range b.N {
					tick()
				}
				after := usageCPU(b)
				b.StopTimer()
				// Observe resident pages and live heap with all proxy streams and the WS still open.
				runtime.GC()
				var mem runtime.MemStats
				runtime.ReadMemStats(&mem)
				b.ReportMetric(float64(mem.HeapAlloc)/(1<<20), "live-heap-MiB")
				b.ReportMetric(float64(usageRSS(b))/(1<<20), "RSS-MiB")
				b.ReportMetric(float64(after-before)/float64(b.N), "cpu-ns/op")
				b.ReportMetric(float64(size.send), "WS-send-B/frame")
				b.ReportMetric(float64(size.receive), "WS-ack-B/frame")
			})
		}
	}
}

func usageRSS(b *testing.B) int64 {
	b.Helper()
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		b.Fatal(err)
	}
	pages, err := strconv.ParseInt(strings.Fields(string(raw))[1], 10, 64)
	if err != nil {
		b.Fatal(err)
	}
	return pages * int64(os.Getpagesize())
}

func usageCPU(b *testing.B) int64 {
	b.Helper()
	var r syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &r); err != nil {
		b.Fatal(err)
	}
	return r.Utime.Nano() + r.Stime.Nano()
}

func usageBenchmarkNode(b *testing.B, kind string) *Service {
	b.Helper()
	server, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { server.Close() })
	go func() {
		for {
			c, err := server.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	reserve, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	echoPort := server.Addr().(*net.TCPAddr).Port
	cfg := config.KernelConfig{Type: kind, ConfigDir: b.TempDir(), LogLevel: "fatal"}
	node := &model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: port, AccessPolicyVersion: 1, AcceptProxyProtocol: new(true), TrustedProxyIPs: []string{"127.0.0.1"}}
	var core kernel.Kernel
	if kind == "xray" {
		cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"redirect": server.Addr().String(), "finalRules": []map[string]any{{"action": "allow", "network": "tcp", "ip": []string{"127.0.0.1/32"}, "port": strconv.Itoa(echoPort)}}}}}
		core = xray.New(cfg)
	} else {
		cfg.CustomOutbound = []map[string]any{{"tag": "direct", "type": "direct"}}
		cfg.CustomRoute = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": echoPort}}
		core = singbox.New(cfg)
	}
	users := make([]model.UserSpec, 1024)
	for i := range users {
		id, err := uuid.NewV4()
		if err != nil {
			b.Fatal(err)
		}
		users[i] = model.UserSpec{ID: i + 1, UUID: id.String()}
	}
	core = directwg.Wrap(core)
	if err := core.Start(node, users, kernel.TLSCert{}); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(core.Stop)
	for i, user := range users[:128] {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, err = fmt.Fprintf(conn, "PROXY TCP4 8.8.4.%d 127.0.0.1 %d %d\r\n", i+1, 30000+i, port)
		if err != nil {
			b.Fatal(err)
		}
		id := uuid.Must(uuid.FromString(user.UUID))
		header := append([]byte{0}, id.Bytes()...)
		header = append(header, 0, 1)
		header = binary.BigEndian.AppendUint16(header, uint16(echoPort))
		header = append(header, 1, 198, 51, 100, 10, 'x')
		if _, err := conn.Write(header); err != nil {
			b.Fatal(err)
		}
		var reply [3]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || !bytes.Equal(reply[:], []byte{0, 0, 'x'}) {
			b.Fatalf("%s echo: %v", kind, err)
		}
		conn.SetDeadline(time.Time{})
	}
	traffic, ips, connCount, err := core.GetUserTraffic(b.Context())
	if err != nil || connCount != 128 || len(ips) != 128 || traffic[128] != [2]int64{1, 1} {
		b.Fatalf("invalid traffic fixture: streams=%d online=%d last=%v err=%v", connCount, len(ips), traffic[128], err)
	}
	s, _ := newShutdownReportService()
	s.kernel, s.lastConfig = core, node
	s.prepareUserState(users)
	s.operations.snapshot.Store(&operationsSnapshot{Run: "running", Version: 1})
	return s
}

func usageBenchmarkPanel(b *testing.B, s *Service, viewers int) <-chan stateExchangeSize {
	b.Helper()
	frames := make(chan stateExchangeSize, 8)
	ids := make([]int, viewers)
	for i := range ids {
		ids[i] = i + 1
	}
	demand := map[string]any{"detail_interval": 60, "user_speeds": false, "omit_device_details": true, "lease_seconds": 60}
	legacy := os.Getenv("YZ_USAGE_BENCH_LEGACY") == "1"
	if !legacy {
		demand["user_usage_version"], demand["user_usage_ids"] = 1, ids
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/handshake"):
			_, _ = w.Write([]byte(`{"host_monitoring":false,"realtime":{"version":1,"traffic_ack":true}}`))
		case strings.HasSuffix(r.URL.Path, "/begin"):
			_, _ = w.Write([]byte(`{"data":{"epoch":"bench"}}`))
		case r.URL.Path == "/ws":
			upgrader := websocket.Upgrader{}
			ws, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				b.Error(err)
				return
			}
			defer ws.Close()
			for _, ev := range []string{"auth.success", "sync.ready"} {
				if err := ws.WriteJSON(map[string]string{"event": ev}); err != nil {
					b.Error(err)
					return
				}
			}
			tcp := ws.UnderlyingConn().(*measuredConn)
			for i := 0; ; i++ {
				rd, wr := tcp.read.Load(), tcp.written.Load()
				var msg struct {
					Data struct {
						panel.StateVersion
						Request string `json:"request_id"`
						Node    int    `json:"node_id"`
						State   struct {
							Online map[int]int `json:"online"`
							Count  *int        `json:"online_count"`
							Usage  *struct {
								Users map[int]any `json:"users"`
							} `json:"user_usage"`
						}
					}
				}
				if err := ws.ReadJSON(&msg); err != nil {
					return
				}
				state := msg.Data.State
				if i >= 2 {
					if legacy && len(state.Online) != 128 {
						b.Error("legacy online population differs")
					}
					if !legacy && (len(state.Online) != 0 || state.Count == nil || *state.Count != 128) {
						b.Error("negotiated summary differs")
					}
					if !legacy && viewers > 0 && (state.Usage == nil || len(state.Usage.Users) != viewers) {
						b.Error("selected detail population differs")
					}
					if !legacy && viewers == 0 && state.Usage != nil {
						b.Error("unsolicited detail")
					}
				}
				receipt := map[string]any{"accepted": true, "epoch": msg.Data.Epoch, "sequence": msg.Data.Sequence, "request_id": msg.Data.Request, "node_id": msg.Data.Node, "telemetry": demand}
				if err := ws.WriteJSON(map[string]any{"event": "state.ack", "data": receipt}); err != nil {
					b.Error(err)
					return
				}
				frames <- stateExchangeSize{send: tcp.read.Load() - rd, receive: tcp.written.Load() - wr}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	server.Listener = &measuredListener{server.Listener}
	server.Start()
	b.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(b.Context())
	ready := make(chan struct{}, 1)
	ws := panel.NewWSClient("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", "", 1, panel.WSClientConfig{Realtime: true, MachineID: 1}, nil, func(st panel.WSStatusChange) {
		if st.Connected {
			select {
			case ready <- struct{}{}:
			default:
			}
		}
	}, nil)
	done := make(chan struct{})
	go func() { defer close(done); ws.Run(ctx) }()
	b.Cleanup(func() { cancel(); <-done })
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		b.Fatal("WebSocket was not ready")
	}
	client := panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1, NodeID: 1})
	if _, err := client.HandshakeContext(ctx); err != nil {
		b.Fatal(err)
	}
	client.SetWebSocketProvider(func() *panel.WSClient { return ws })
	cp := controlplane.NewMachinePanelControlPlane(client, nil, nil)
	s.source, s.sink = cp, cp
	return frames
}
