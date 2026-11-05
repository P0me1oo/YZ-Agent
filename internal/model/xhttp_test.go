package model

import (
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"testing"
)

func TestXHTTPVisionValidation(t *testing.T) {
	n := &NodeSpec{Protocol: "vless", Network: "xhttp", Flow: "xtls-rprx-vision"}
	if ValidateNodeSpec(n, config.KernelConfig{Type: "xray"}) == nil {
		t.Fatal("Vision without Encryption accepted")
	}
	n.Flow = ""
	if err := ValidateNodeSpec(n, config.KernelConfig{Type: "xray"}); err != nil {
		t.Fatal(err)
	}
	n.TLS = 1
	n.TLSSettings = map[string]any{"alpn": []string{"h3"}}
	if !n.UsesXHTTP3() || n.SupportsProxyProtocol() {
		t.Fatal("H3 listener classified as TCP")
	}
	if err := ValidateNodeSpec(n, config.KernelConfig{Type: "xray"}); err != nil {
		t.Fatal(err)
	}
	n.TLSSettings["alpn"] = []string{"h3", "h2"}
	if ValidateNodeSpec(n, config.KernelConfig{Type: "xray"}) == nil {
		t.Fatal("mixed H3 ALPN accepted")
	}
}
