package model

import (
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"os"
	"path/filepath"
	"testing"
)

func TestNextAccessPolicyRoundTripAndValidation(t *testing.T) {
	var wire panel.NodeConfig
	if err := json.Unmarshal([]byte(`{"access_policy_version":1,"accept_proxy_protocol":true,"trusted_proxy_ips":["192.0.2.1"],"device_ip_exclude":["8.8.8.8"]}`), &wire); err != nil {
		t.Fatal(err)
	}
	spec := NodeSpecFromPanel(&wire)
	if err := validateAccessPolicy(spec); err != nil {
		t.Fatal(err)
	}
	wire.TrustedProxyIPs[0] = "0.0.0.0/0"
	if spec.TrustedProxyIPs[0] != "192.0.2.1" || spec.ToPanel().AccessPolicyVersion != 1 {
		t.Fatal("policy alias or missing version")
	}
	spec.TrustedProxyIPs = nil
	if validateAccessPolicy(spec) == nil {
		t.Fatal("trust-all fallback")
	}
	spec.AccessPolicyVersion = 2
	if validateAccessPolicy(spec) == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestNextLocalRoutingOverrideRejected(t *testing.T) {
	node := &NodeSpec{AccessPolicyVersion: 1, Protocol: "vless", Routes: []RouteRule{{ID: 1, Action: "block", Match: []string{"example.com"}}}}
	if err := ValidateNodeSpec(node, config.KernelConfig{Type: "xray", CustomRoute: []map[string]any{{"outboundTag": "direct"}}}); err == nil {
		t.Fatal("local routes bypass Next policy")
	}
	path := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(path, []byte(`{"routing":{"rules":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if validateNextLocalRoutes(path) == nil {
		t.Fatal("local routing object accepted")
	}
	if err := os.WriteFile(path, []byte(`{"dns":{"servers":["localhost"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateNextLocalRoutes(path); err != nil {
		t.Fatal(err)
	}
}
