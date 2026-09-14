package xray

import (
	"bytes"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/xtls/xray-core/infra/conf/serial"
)

func relayEntryNode() *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:   "vless",
		ServerPort: 24443,
		Network:    "tcp",
		TLS:        2,
		Flow:       "xtls-rprx-vision",
		TLSSettings: map[string]interface{}{
			"private_key": "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdef0",
			"server_name": "www.example.com",
			"short_id":    "0123abcd",
		},
		Relay: &panel.RelayConfig{
			Mode:    panel.RelayModeEntry,
			RouteID: 11,
			Children: []panel.RelayChild{
				{
					NodeID:   7,
					Tag:      "relay-7",
					RouteID:  12,
					Protocol: "shadowsocks",
					Address:  "203.0.113.7",
					Port:     28388,
					Cipher:   "2022-blake3-aes-128-gcm",
					Password: "MTIzNDU2Nzg5MGFiY2RlZg==",
				},
				{
					NodeID:   9,
					Tag:      "relay-9",
					RouteID:  13,
					Protocol: "shadowsocks",
					Address:  "203.0.113.9",
					Port:     28389,
					Cipher:   "aes-128-gcm",
					Password: "0123456789abcdef0123456789abcdef",
				},
			},
		},
	}
}

func relayLandingNode() *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:   "shadowsocks",
		ServerPort: 28388,
		Cipher:     "2022-blake3-aes-128-gcm",
		Relay: &panel.RelayConfig{
			Mode:        panel.RelayModeLanding,
			Protocol:    "shadowsocks",
			ListenPort:  28388,
			Cipher:      "2022-blake3-aes-128-gcm",
			Password:    "MTIzNDU2Nzg5MGFiY2RlZg==",
			EntryNodeID: 3,
		},
	}
}

func relayVLESSChild(network string, tlsMode int) panel.RelayChild {
	settings := map[string]interface{}{}
	switch network {
	case "ws", "httpupgrade", "xhttp":
		settings["path"] = "/relay"
		settings["host"] = "landing.example.com"
	case "grpc":
		settings["serviceName"] = "relay"
	}

	return panel.RelayChild{
		NodeID:   17,
		Tag:      "relay-17",
		RouteID:  17,
		Protocol: "vless",
		Address:  "10.0.0.17",
		Port:     29388,
		VLESS: &panel.RelayVLESSConfig{
			ID:              "11111111-2222-0000-8444-555555555555",
			Network:         network,
			NetworkSettings: settings,
			TLS:             tlsMode,
			Encryption:      "none",
			TLSSettings: map[string]interface{}{
				"server_name": "landing.example.com",
				"fingerprint": "chrome",
			},
			RealitySettings: map[string]interface{}{
				"server_name": "landing.example.com",
				"public_key":  "TESTonlyPUBLICkeyNOTaREALsecret0123456789ab",
				"short_id":    "89abcdef",
				"fingerprint": "chrome",
			},
			TransportAuth: "relay-hysteria-test-auth",
		},
	}
}

func relayVLESSLandingNode(network string, tlsMode int) *panel.NodeConfig {
	return &panel.NodeConfig{
		Protocol:        "vless",
		ServerPort:      29388,
		Network:         network,
		NetworkSettings: relayVLESSChild(network, tlsMode).VLESS.NetworkSettings,
		TLS:             tlsMode,
		Decryption:      "none",
		TLSSettings: map[string]interface{}{
			"private_key": "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789abcdef0",
			"server_name": "landing.example.com",
			"short_id":    "89abcdef",
		},
		Relay: &panel.RelayConfig{
			Mode:        panel.RelayModeLanding,
			Protocol:    "vless",
			ListenPort:  29388,
			EntryNodeID: 3,
			VLESS: &panel.RelayVLESSConfig{
				ID:            "11111111-2222-0000-8444-555555555555",
				TransportAuth: "relay-hysteria-test-auth",
			},
		},
	}
}

// The entry must keep exactly one client inbound no matter how many logical
// nodes hang off it — that is the whole point of the single-entry design.
func TestBuildConfig_RelayEntrySingleInbound(t *testing.T) {
	cfg := buildConfig(testKernelCfg, testNodeSpec(relayEntryNode()), testUsers, kernel.TLSCert{})

	inbounds, ok := cfg["inbounds"].([]M)
	if !ok {
		t.Fatalf("inbounds missing: %#v", cfg["inbounds"])
	}
	if len(inbounds) != 1 {
		t.Fatalf("inbounds = %d, want 1", len(inbounds))
	}
	if got := inbounds[0]["protocol"]; got != "vless" {
		t.Fatalf("inbound protocol = %v, want vless", got)
	}
	if got := inbounds[0]["port"]; got != 24443 {
		t.Fatalf("inbound port = %v, want 24443", got)
	}
}

func TestBuildConfig_RelayEntryOutboundsAndRoutes(t *testing.T) {
	cfg := buildConfig(testKernelCfg, testNodeSpec(relayEntryNode()), testUsers, kernel.TLSCert{})

	outbounds, _ := cfg["outbounds"].([]M)
	byTag := map[string]M{}
	for _, ob := range outbounds {
		tag, _ := ob["tag"].(string)
		byTag[tag] = ob
	}

	for _, tag := range []string{"direct", "block", "relay-7", "relay-9"} {
		if _, ok := byTag[tag]; !ok {
			t.Fatalf("outbound %q missing, got %v", tag, keysOf(byTag))
		}
	}
	if got := byTag["relay-7"]["protocol"]; got != "shadowsocks" {
		t.Fatalf("relay-7 protocol = %v, want shadowsocks", got)
	}

	settings, _ := byTag["relay-7"]["settings"].(M)
	servers, _ := settings["servers"].([]M)
	if len(servers) != 1 {
		t.Fatalf("relay-7 servers = %d, want 1", len(servers))
	}
	if servers[0]["address"] != "203.0.113.7" || servers[0]["port"] != 28388 {
		t.Fatalf("relay-7 target = %v:%v, want 203.0.113.7:28388", servers[0]["address"], servers[0]["port"])
	}

	routing, _ := cfg["routing"].(M)
	rules, _ := routing["rules"].([]M)
	want := map[string]string{"11": "direct", "12": "relay-7", "13": "relay-9"}
	found := map[string]string{}
	for _, rule := range rules {
		route, ok := rule["vlessRoute"].(string)
		if !ok {
			continue
		}
		tag, _ := rule["outboundTag"].(string)
		found[route] = tag
	}
	if len(found) != len(want) {
		t.Fatalf("vlessRoute rules = %v, want %v", found, want)
	}
	for route, tag := range want {
		if found[route] != tag {
			t.Fatalf("route %s -> %q, want %q", route, found[route], tag)
		}
	}
}

// The generated JSON must survive xray's own parser, otherwise the entry would
// only fail at kernel start time.
func TestBuildConfig_RelayEntryParsesAsXrayConfig(t *testing.T) {
	raw, err := marshalConfig(testKernelCfg, testNodeSpec(relayEntryNode()), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshalConfig: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(raw)); err != nil {
		t.Fatalf("xray rejected relay entry config: %v", err)
	}
}

// Repeated builds must be byte-identical, so a re-sync or reload never produces
// duplicate outbounds or routing rules.
func TestBuildConfig_RelayEntryIdempotent(t *testing.T) {
	spec := testNodeSpec(relayEntryNode())
	first, err := marshalConfig(testKernelCfg, spec, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	second, err := marshalConfig(testKernelCfg, spec, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("relay entry config is not stable across builds")
	}
}

// Removing a logical node must drop exactly its outbound and its routing rule
// while leaving the client inbound and the remaining logical node untouched.
func TestBuildConfig_RelayEntryDropsRemovedChild(t *testing.T) {
	nc := relayEntryNode()
	nc.Relay.Children = nc.Relay.Children[:1]
	cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	outbounds, _ := cfg["outbounds"].([]M)
	for _, ob := range outbounds {
		if ob["tag"] == "relay-9" {
			t.Fatalf("outbound relay-9 should be gone")
		}
	}

	routing, _ := cfg["routing"].(M)
	rules, _ := routing["rules"].([]M)
	for _, rule := range rules {
		if rule["vlessRoute"] == "13" {
			t.Fatalf("routing rule for removed child should be gone")
		}
	}

	inbounds, _ := cfg["inbounds"].([]M)
	if len(inbounds) != 1 || inbounds[0]["protocol"] != "vless" {
		t.Fatalf("client inbound was affected by child removal: %#v", inbounds)
	}
}

// A landing node exposes only the internal inbound and must not embed any panel
// user, otherwise the landing side would re-count user traffic.
func TestBuildConfig_RelayLandingInbound(t *testing.T) {
	cfg := buildConfig(testKernelCfg, testNodeSpec(relayLandingNode()), testUsers, kernel.TLSCert{})

	inbounds, ok := cfg["inbounds"].([]M)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds = %#v, want exactly 1", cfg["inbounds"])
	}
	in := inbounds[0]
	if in["tag"] != relayLandingInboundTag {
		t.Fatalf("inbound tag = %v, want %v", in["tag"], relayLandingInboundTag)
	}
	if in["port"] != 28388 {
		t.Fatalf("inbound port = %v, want 28388", in["port"])
	}

	settings, _ := in["settings"].(M)
	if settings["password"] != "MTIzNDU2Nzg5MGFiY2RlZg==" {
		t.Fatalf("landing password not taken from relay credential: %v", settings["password"])
	}
	if settings["network"] != "tcp,udp" {
		t.Fatalf("landing network = %v, want tcp,udp", settings["network"])
	}
	if _, hasClients := settings["clients"]; hasClients {
		t.Fatalf("landing inbound must not carry panel users")
	}

	raw, err := marshalConfig(testKernelCfg, testNodeSpec(relayLandingNode()), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshalConfig: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(raw)); err != nil {
		t.Fatalf("xray rejected relay landing config: %v", err)
	}
}

func TestBuildConfig_RelayVLESSTransportMatrixParses(t *testing.T) {
	cases := []struct {
		network string
		tls     int
	}{
		{"tcp", 0}, {"tcp", 1}, {"tcp", 2},
		{"ws", 0}, {"ws", 1},
		{"grpc", 0}, {"grpc", 1}, {"grpc", 2},
		{"xhttp", 0}, {"xhttp", 1}, {"xhttp", 2},
		{"httpupgrade", 0}, {"httpupgrade", 1},
		{"kcp", 0}, {"kcp", 1},
		{"hysteria", 1},
	}

	for _, tc := range cases {
		t.Run(tc.network+"-tls-"+string(rune('0'+tc.tls)), func(t *testing.T) {
			entry := relayEntryNode()
			entry.Relay.Children = []panel.RelayChild{relayVLESSChild(tc.network, tc.tls)}
			entryRaw, err := marshalConfig(testKernelCfg, testNodeSpec(entry), testUsers, kernel.TLSCert{})
			if err != nil {
				t.Fatalf("marshal entry: %v", err)
			}
			if _, err := serial.LoadJSONConfig(bytes.NewReader(entryRaw)); err != nil {
				t.Fatalf("xray rejected entry %s tls=%d: %v\n%s", tc.network, tc.tls, err, entryRaw)
			}

			landing := relayVLESSLandingNode(tc.network, tc.tls)
			landingRaw, err := marshalConfig(testKernelCfg, testNodeSpec(landing), nil, kernel.TLSCert{})
			if err != nil {
				t.Fatalf("marshal landing: %v", err)
			}
			if _, err := serial.LoadJSONConfig(bytes.NewReader(landingRaw)); err != nil {
				t.Fatalf("xray rejected landing %s tls=%d: %v\n%s", tc.network, tc.tls, err, landingRaw)
			}
		})
	}
}

func TestBuildConfig_RelayEntryTransportMatrixParses(t *testing.T) {
	cases := []struct {
		network string
		tls     int
	}{
		{"tcp", 0}, {"tcp", 1}, {"tcp", 2},
		{"ws", 0}, {"ws", 1},
		{"grpc", 0}, {"grpc", 1}, {"grpc", 2},
		{"xhttp", 0}, {"xhttp", 1}, {"xhttp", 2},
		{"httpupgrade", 0}, {"httpupgrade", 1},
		{"kcp", 0}, {"kcp", 1},
		{"hysteria", 1},
	}

	for _, tc := range cases {
		t.Run(tc.network+"-tls-"+string(rune('0'+tc.tls)), func(t *testing.T) {
			entry := relayEntryNode()
			entry.Network = tc.network
			entry.NetworkSettings = relayVLESSChild(tc.network, tc.tls).VLESS.NetworkSettings
			entry.TLS = tc.tls
			entry.Flow = "none"

			raw, err := marshalConfig(testKernelCfg, testNodeSpec(entry), testUsers, kernel.TLSCert{})
			if err != nil {
				t.Fatalf("marshal entry: %v", err)
			}
			if _, err := serial.LoadJSONConfig(bytes.NewReader(raw)); err != nil {
				t.Fatalf("xray rejected relay entry %s tls=%d: %v\n%s", tc.network, tc.tls, err, raw)
			}
		})
	}
}

func TestBuildConfig_RelayVLESSSupportsEncryption(t *testing.T) {
	const encryption = "mlkem768x25519plus.native.0rtt.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const decryption = "mlkem768x25519plus.native.600s.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	entry := relayEntryNode()
	child := relayVLESSChild("tcp", 0)
	child.VLESS.Encryption = encryption
	entry.Relay.Children = []panel.RelayChild{child}
	entryRaw, err := marshalConfig(testKernelCfg, testNodeSpec(entry), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(entryRaw)); err != nil {
		t.Fatalf("xray rejected VLESS Encryption outbound: %v", err)
	}

	landing := relayVLESSLandingNode("tcp", 0)
	landing.Decryption = decryption
	landingRaw, err := marshalConfig(testKernelCfg, testNodeSpec(landing), nil, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshal landing: %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(landingRaw)); err != nil {
		t.Fatalf("xray rejected VLESS Encryption inbound: %v", err)
	}
}

// A plain node must be unaffected by the relay feature.
func TestBuildConfig_NoRelayLeavesRoutingUntouched(t *testing.T) {
	nc := relayEntryNode()
	nc.Relay = nil
	cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	routing, _ := cfg["routing"].(M)
	rules, _ := routing["rules"].([]M)
	for _, rule := range rules {
		if _, ok := rule["vlessRoute"]; ok {
			t.Fatalf("plain node must not get vlessRoute rules")
		}
	}
	outbounds, _ := cfg["outbounds"].([]M)
	if len(outbounds) != 2 {
		t.Fatalf("outbounds = %d, want direct + block only", len(outbounds))
	}
}

func TestNodeSpec_RelayHelpers(t *testing.T) {
	spec := testNodeSpec(relayEntryNode())
	if !spec.IsRelayEntry() || spec.IsRelayLanding() {
		t.Fatalf("entry spec misclassified")
	}
	tags := spec.RelayOutboundTags()
	if len(tags) != 2 || tags[0] != "relay-7" || tags[1] != "relay-9" {
		t.Fatalf("RelayOutboundTags = %v", tags)
	}
	byTag := spec.RelayNodeIDByTag()
	if byTag["relay-7"] != 7 || byTag["relay-9"] != 9 {
		t.Fatalf("RelayNodeIDByTag = %v", byTag)
	}

	landing := testNodeSpec(relayLandingNode())
	if landing.IsRelayEntry() || !landing.IsRelayLanding() {
		t.Fatalf("landing spec misclassified")
	}

	var nilSpec *model.NodeSpec
	if nilSpec.IsRelayEntry() || nilSpec.IsRelayLanding() {
		t.Fatalf("nil spec must not be a relay node")
	}
}

func keysOf(m map[string]M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
