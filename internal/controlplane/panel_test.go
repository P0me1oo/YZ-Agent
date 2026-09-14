package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	panelapi "github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestControlPlaneInitialPreservesInvalidCustomOutbounds(t *testing.T) {
	server := newPanelTestServer(`{"protocol":"shadowsocks","server_port":8388,"custom_outbounds":[{"tag":"proxy","protocol":"socks","proxy_tag":"missing","settings":{"server":"2.2.2.2","server_port":1080}}]}`)
	defer server.Close()

	for name, cp := range snapshotControlPlanes(server.URL) {
		t.Run(name, func(t *testing.T) {
			snapshot, err := cp.Initial(context.Background(), nil, nil, nil)
			if err != nil {
				t.Fatalf("无效配置应交给节点处理，不能结束控制通道: %v", err)
			}
			assertInvalidOutboundSnapshot(t, snapshot.Config, "singbox", `proxy_tag references unknown outbound "missing"`)
			if snapshot.Users == nil {
				t.Fatal("初始用户快照丢失")
			}
		})
	}
}

func TestControlPlanePollPreservesInvalidCustomOutbounds(t *testing.T) {
	server := newPanelTestServer(`{"protocol":"shadowsocks","server_port":8388,"custom_outbounds":[{"tag":"proxy","protocol":"socks","proxy_tag":"missing","settings":{"server":"2.2.2.2","server_port":1080}}]}`)
	defer server.Close()

	for name, cp := range snapshotControlPlanes(server.URL) {
		t.Run(name, func(t *testing.T) {
			snapshot, err := cp.Poll(context.Background())
			if err != nil {
				t.Fatalf("已获取的无效配置不能伪装成网络错误: %v", err)
			}
			assertInvalidOutboundSnapshot(t, snapshot.Config, "singbox", `proxy_tag references unknown outbound "missing"`)
			if snapshot.Users == nil {
				t.Fatal("轮询用户快照丢失")
			}
		})
	}
}

func snapshotControlPlanes(url string) map[string]ControlPlane {
	cfg := config.PanelConfig{URL: url, NodeID: 1}
	return map[string]ControlPlane{
		"node":    NewPanelControlPlane(cfg, config.WSConfig{}),
		"machine": NewMachinePanelControlPlane(panelapi.NewClient(cfg), nil, nil),
	}
}

func assertInvalidOutboundSnapshot(t *testing.T, spec *model.NodeSpec, kernelType, reason string) {
	t.Helper()
	if spec == nil || len(spec.CustomOutbounds) != 1 {
		t.Fatal("控制通道丢失了待修正出站配置")
	}
	err := model.ValidateNodeSpec(spec, config.KernelConfig{Type: kernelType})
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("节点仍应拒绝该快照，实际校验结果: %v", err)
	}
}

func TestPanelControlPlanePollRestoresETagsWhenUsersFail(t *testing.T) {
	configCalls := 0
	userCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		configCalls++
		if configCalls == 2 && r.Header.Get("If-None-Match") != "" {
			t.Fatalf("second config request reused uncommitted ETag %q", r.Header.Get("If-None-Match"))
		}
		w.Header().Set("ETag", `"config-v2"`)
		_, _ = w.Write([]byte(`{"protocol":"shadowsocks","server_port":8388}`))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		userCalls++
		if userCalls == 1 {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", `"users-v2"`)
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cp := NewPanelControlPlane(config.PanelConfig{URL: server.URL, NodeID: 1}, config.WSConfig{})
	if _, err := cp.Poll(context.Background()); err == nil {
		t.Fatal("expected first poll to fail")
	}
	snapshot, err := cp.Poll(context.Background())
	if err != nil {
		t.Fatalf("second poll failed: %v", err)
	}
	if snapshot.Config == nil || snapshot.Config.ServerPort != 8388 {
		t.Fatalf("unexpected snapshot: %#v", snapshot.Config)
	}
}

func TestPanelControlPlanePollAcceptsNotModifiedSnapshot(t *testing.T) {
	configCalls := 0
	userCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		configCalls++
		if configCalls > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"config-v1"`)
		_, _ = w.Write([]byte(`{"protocol":"shadowsocks","server_port":8388}`))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		userCalls++
		if userCalls > 1 {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"users-v1"`)
		_, _ = w.Write([]byte(`{"users":[]}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cp := NewPanelControlPlane(config.PanelConfig{URL: server.URL, NodeID: 1}, config.WSConfig{})
	if _, err := cp.Poll(context.Background()); err != nil {
		t.Fatalf("first poll failed: %v", err)
	}
	snapshot, err := cp.Poll(context.Background())
	if err != nil {
		t.Fatalf("304 poll failed: %v", err)
	}
	if snapshot.Config != nil || snapshot.Users != nil {
		t.Fatalf("expected unchanged snapshot, got %#v", snapshot)
	}
}

func TestTranslateWSEventPreservesInvalidCustomOutbounds(t *testing.T) {
	event := TranslateWSEvent(panelapi.WSEvent{
		Type: panelapi.WSEventSyncConfig,
		Config: &panelapi.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panelapi.OutboundConfig{
				{Tag: "proxy", Protocol: "socks", ProxyTag: "missing", Settings: map[string]any{"server": "2.2.2.2", "server_port": 1080}},
			},
		},
	})
	assertInvalidOutboundSnapshot(t, event.Config, "singbox", `proxy_tag references unknown outbound "missing"`)
}

func newPanelTestServer(configBody string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/server/handshake", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"websocket":{"enabled":false},"settings":{"push_interval":60,"pull_interval":60}}`))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(configBody))
	})
	mux.HandleFunc("/api/v1/server/UniProxy/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"11111111-1111-1111-1111-111111111111"}]}`))
	})
	return httptest.NewServer(mux)
}

func TestTranslateWSEventPreservesUnsupportedProtocolForRuntimeValidation(t *testing.T) {
	event := TranslateWSEvent(panelapi.WSEvent{
		Type: panelapi.WSEventSyncConfig,
		Config: &panelapi.NodeConfig{
			Protocol:   "shadowsocks",
			ServerPort: 8388,
			CustomOutbounds: []panelapi.OutboundConfig{
				{Tag: "hy2", Protocol: "hysteria2", Settings: map[string]any{"server": "2.2.2.2", "server_port": 8443}},
			},
		},
	})
	assertInvalidOutboundSnapshot(t, event.Config, "xray", `protocol "hysteria2" is not supported by kernel "xray"`)
}
