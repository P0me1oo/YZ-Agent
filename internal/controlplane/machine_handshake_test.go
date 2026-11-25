package controlplane

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	panelapi "github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestMachineInitialWaitsForCoordinationHandshake(t *testing.T) {
	var handshakeReady atomic.Bool
	var snapshotCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v2/server/handshake" {
			if !handshakeReady.Load() {
				http.Error(w, "temporary handshake failure", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"realtime":{"device_handover":1,"device_lease_cache":1}}`))
			return
		}
		snapshotCalls.Add(1)
		if r.URL.Path == "/api/v1/server/UniProxy/config" {
			_, _ = w.Write([]byte(`{"protocol":"shadowsocks","server_port":8388}`))
			return
		}
		_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"test","device_limit":1}]}`))
	}))
	defer server.Close()
	client := panelapi.NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	cp := NewMachinePanelControlPlane(client, nil, nil)
	if _, err := cp.Initial(t.Context(), nil, nil, nil); err == nil {
		t.Fatal("failed handshake started a node without global coordination")
	}
	if snapshotCalls.Load() != 0 {
		t.Fatal("failed handshake continued to unconfirmed user snapshots")
	}
	handshakeReady.Store(true)
	snapshot, err := cp.Initial(t.Context(), nil, nil, nil)
	if err != nil || snapshot.Config == nil || len(snapshot.Users) != 1 {
		t.Fatalf("initialization did not recover: %v", err)
	}
	if !client.DeviceHandoverSupported() {
		t.Fatal("recovered initialization omitted the global gate")
	}
}
