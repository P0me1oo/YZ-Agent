package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

// 同一端口同时监听回环 IPv4 和 IPv6，用 localhost 访问时由调用方决定走哪种地址族。
func newDualStackServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	return server, "http://localhost:" + port
}

func TestReportMachineAddressUsesRequestedFamily(t *testing.T) {
	server, url := newDualStackServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/server/machine/address" || r.URL.RawQuery != "" {
			t.Error("unexpected request")
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["token"] != "machine-token" || body["machine_id"] != float64(7) {
			t.Error("missing machine auth")
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_ = json.NewEncoder(w).Encode(map[string]string{"ip": host})
	})
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: url, Token: "machine-token", MachineID: 7})
	for _, tc := range []struct{ network, loopback string }{{"tcp4", "127.0.0.1"}, {"tcp6", "::1"}} {
		t.Run(tc.network, func(t *testing.T) {
			if tc.network == "tcp6" {
				probe, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					t.Skip("IPv6 loopback unavailable")
				}
				probe.Close()
			}
			ip, err := client.ReportMachineAddress(context.Background(), tc.network)
			if err != nil || ip != tc.loopback {
				t.Fatalf("ip=%q err=%v", ip, err)
			}
		})
	}
}

func TestReportMachineAddressOldPanelAndErrors(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		ip, err := client.ReportMachineAddress(context.Background(), "tcp4")
		server.Close()
		if ip != "" || err == nil || errors.Is(err, ErrMachineAddressUnsupported) != (code == http.StatusNotFound) {
			t.Fatalf("status %d: ip=%q err=%v", code, ip, err)
		}
	}
}

func TestReportMachineAddressNonPublicSourceReturnsEmpty(t *testing.T) {
	server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ip":null}`))
	})
	defer server.Close()
	ip, err := client.ReportMachineAddress(context.Background(), "tcp4")
	if err != nil || ip != "" {
		t.Fatalf("ip=%q err=%v", ip, err)
	}
}
