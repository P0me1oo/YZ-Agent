package machine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func newAddressTestServer(t *testing.T, status int) (*httptest.Server, string, func() []string) {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sources []string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		mu.Lock()
		sources = append(sources, host)
		mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"ip":null}`))
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sources...)
	}
	return server, "http://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), seen
}

func runAddressLoop(t *testing.T, url string) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	orchestrator := &Orchestrator{client: panel.NewClient(config.PanelConfig{URL: url, Token: "machine-token", MachineID: 7})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		orchestrator.addressLoop(ctx)
		close(done)
	}()
	return cancel, done
}

func TestAddressLoopReportsBothFamiliesAtStartup(t *testing.T) {
	if probe, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Skip("IPv6 loopback unavailable")
	} else {
		probe.Close()
	}
	server, url, seen := newAddressTestServer(t, http.StatusOK)
	defer server.Close()
	cancel, done := runAddressLoop(t, url)
	deadline := time.Now().Add(5 * time.Second)
	for len(seen()) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if got := seen(); len(got) != 2 || got[0] != "127.0.0.1" || got[1] != "::1" {
		t.Fatalf("sources = %v, want one IPv4 and one IPv6 report", got)
	}
}

func TestAddressLoopBacksOffOnOldPanel(t *testing.T) {
	server, url, seen := newAddressTestServer(t, http.StatusNotFound)
	defer server.Close()
	cancel, done := runAddressLoop(t, url)
	deadline := time.Now().Add(5 * time.Second)
	for len(seen()) < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// 旧面板返回 404 后，本轮不再尝试另一地址族，也不会立即重试。
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if got := seen(); len(got) != 1 {
		t.Fatalf("requests = %v, want a single report before backing off", got)
	}
}
