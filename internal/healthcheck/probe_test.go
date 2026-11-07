package healthcheck

import (
	"context"
	"testing"
)

func TestRejectNonPublicAndCredentialTargets(t *testing.T) {
	for _, raw := range []string{"http://example.com/", "https://user:secret@example.com/", "https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/", "https://100.100.100.200/", "https://192.168.1.1/"} {
		if _, _, err := Target(context.Background(), raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestListenerTransportMatchesHTTP3AndUDPProtocols(t *testing.T) {
	for _, tc := range []struct {
		protocol, network string
		tls               int
		settings          map[string]any
		tcp               bool
	}{
		{"vless", "xhttp", 1, map[string]any{"alpn": []string{"h3"}}, false},
		{"vless", "xhttp", 1, map[string]any{"alpn": []string{"h2"}}, true},
		{"vless", "tcp", 2, nil, true},
		{"vmess", "kcp", 0, nil, false},
		{"hysteria2", "", 1, nil, false},
		{"tuic", "", 1, nil, false},
		{"wireguard", "", 0, nil, false},
	} {
		if IsTCP(tc.protocol, tc.network, tc.tls, tc.settings) != tc.tcp {
			t.Fatalf("错误的监听探测类型: %+v", tc)
		}
	}
}
