package xray

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestShadowsocksPluginStream(t *testing.T) {
	n := &model.NodeSpec{Protocol: "shadowsocks", ServerPort: 12345, Cipher: "2022-blake3-aes-128-gcm", Plugin: "v2ray-plugin", PluginOpt: "mode=websocket;mux=0", Network: "ws", NetworkSettings: map[string]any{"path": "/ss"}, TLS: 1, TLSSettings: map[string]any{"server_name": "example.com"}}
	c := buildInbound(config.KernelConfig{}, n, nil, kernel.TLSCert{CertPEM: []byte("cert"), KeyPEM: []byte("key")})
	stream, ok := c["streamSettings"].(M)
	if !ok {
		t.Fatal("plugin transport omitted")
	}
	if stream["network"] != "ws" || stream["security"] != "tls" {
		t.Fatal("plugin transport or TLS not applied")
	}
}
