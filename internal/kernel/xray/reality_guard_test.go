package xray

import (
	"bytes"
	"fmt"
	"net"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// guardNodeConfig 在标准 REALITY 节点基础上补上伪装回源端口，anti 控制是否打开防盗用模式。
func guardNodeConfig(anti any) *panel.NodeConfig {
	nc := realityNodeConfig()
	nc.Network = "tcp"
	nc.TLSSettings["dest"] = "www.example.com:8443"
	if anti != nil {
		nc.TLSSettings["anti_abuse"] = anti
	}
	return nc
}

// guardRealitySettings 取出节点入口的 REALITY 配置，方便断言回源地址。
func guardRealitySettings(t *testing.T, inbound M) M {
	t.Helper()
	stream, ok := inbound["streamSettings"].(M)
	if !ok {
		t.Fatalf("streamSettings = %#v, want object", inbound["streamSettings"])
	}
	settings, ok := stream["realitySettings"].(M)
	if !ok {
		t.Fatalf("realitySettings = %#v, want object", stream["realitySettings"])
	}
	return settings
}

func TestApplyRealityGuard_RewritesDestAndAddsInbound(t *testing.T) {
	nc := guardNodeConfig(true)
	cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})

	inbounds, ok := cfg["inbounds"].([]M)
	if !ok || len(inbounds) != 2 {
		t.Fatalf("inbounds = %#v, want node inbound plus guard inbound", cfg["inbounds"])
	}

	guard := inbounds[1]
	if guard["tag"] != realityGuardTag {
		t.Fatalf("guard tag = %#v, want %q", guard["tag"], realityGuardTag)
	}
	if guard["listen"] != "127.0.0.1" {
		t.Fatalf("guard listen = %#v, want 127.0.0.1", guard["listen"])
	}
	if guard["protocol"] != "dokodemo-door" {
		t.Fatalf("guard protocol = %#v, want dokodemo-door", guard["protocol"])
	}

	port, ok := guard["port"].(int)
	if !ok {
		t.Fatalf("guard port = %#v, want int", guard["port"])
	}
	if port < realityGuardPortBase || port >= realityGuardPortBase+realityGuardPortSpan {
		t.Fatalf("guard port = %d, want inside %d-%d", port, realityGuardPortBase, realityGuardPortBase+realityGuardPortSpan-1)
	}
	if port == nc.ServerPort {
		t.Fatalf("guard port = %d, must not reuse the node listen port", port)
	}

	// 回源端口沿用原伪装目标端口，嗅探只改写地址。
	guardSettings, ok := guard["settings"].(M)
	if !ok {
		t.Fatalf("guard settings = %#v, want object", guard["settings"])
	}
	if guardSettings["port"] != 8443 {
		t.Fatalf("guard settings.port = %#v, want 8443", guardSettings["port"])
	}
	if guardSettings["network"] != "tcp" {
		t.Fatalf("guard settings.network = %#v, want tcp", guardSettings["network"])
	}

	sniffing, ok := guard["sniffing"].(M)
	if !ok {
		t.Fatalf("guard sniffing = %#v, want object", guard["sniffing"])
	}
	if sniffing["enabled"] != true || sniffing["routeOnly"] != false {
		t.Fatalf("guard sniffing = %#v, want enabled and routeOnly=false", sniffing)
	}
	destOverride, ok := sniffing["destOverride"].([]string)
	if !ok || len(destOverride) != 1 || destOverride[0] != "tls" {
		t.Fatalf("guard destOverride = %#v, want [tls]", sniffing["destOverride"])
	}

	// 节点入口的回源地址必须改接到这个本机入口。
	settings := guardRealitySettings(t, inbounds[0])
	wantDest := fmt.Sprintf("127.0.0.1:%d", port)
	if settings["dest"] != wantDest {
		t.Fatalf("realitySettings.dest = %#v, want %q", settings["dest"], wantDest)
	}
	// 伪装域名列表不受影响，客户端握手校验口径保持原样。
	names, ok := settings["serverNames"].([]string)
	if !ok || len(names) != 1 || names[0] != "www.example.com" {
		t.Fatalf("serverNames = %#v, want [www.example.com]", settings["serverNames"])
	}

	routing, ok := cfg["routing"].(M)
	if !ok {
		t.Fatalf("routing = %#v, want object", cfg["routing"])
	}
	rules, ok := routing["rules"].([]M)
	if !ok || len(rules) < 2 {
		t.Fatalf("routing rules = %#v, want at least the two guard rules", routing["rules"])
	}

	allow := rules[0]
	if allow["outboundTag"] != "direct" {
		t.Fatalf("first rule outboundTag = %#v, want direct", allow["outboundTag"])
	}
	allowTags, ok := allow["inboundTag"].([]string)
	if !ok || len(allowTags) != 1 || allowTags[0] != realityGuardTag {
		t.Fatalf("first rule inboundTag = %#v, want [%s]", allow["inboundTag"], realityGuardTag)
	}
	domains, ok := allow["domain"].([]string)
	if !ok || len(domains) != 1 || domains[0] != "full:www.example.com" {
		t.Fatalf("first rule domain = %#v, want [full:www.example.com]", allow["domain"])
	}

	deny := rules[1]
	if deny["outboundTag"] != "block" {
		t.Fatalf("second rule outboundTag = %#v, want block", deny["outboundTag"])
	}
	if _, hasDomain := deny["domain"]; hasDomain {
		t.Fatalf("second rule must be the catch-all, got %#v", deny)
	}

	data, err := marshalConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("marshalConfig() error = %v", err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("Xray rejected the anti-abuse config: %v", err)
	}
}

func TestApplyRealityGuard_SkipCases(t *testing.T) {
	cases := []struct {
		name string
		nc   func() *panel.NodeConfig
	}{
		{
			name: "开关未下发",
			nc:   func() *panel.NodeConfig { return guardNodeConfig(nil) },
		},
		{
			name: "开关关闭",
			nc:   func() *panel.NodeConfig { return guardNodeConfig(false) },
		},
		{
			name: "没有伪装域名",
			nc: func() *panel.NodeConfig {
				nc := guardNodeConfig(true)
				delete(nc.TLSSettings, "server_name")
				return nc
			},
		},
		{
			name: "不是REALITY",
			nc: func() *panel.NodeConfig {
				nc := guardNodeConfig(true)
				nc.TLS = 1
				nc.ServerName = "example.com"
				return nc
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nc := tc.nc()
			cfg := buildConfig(testKernelCfg, testNodeSpec(nc), testUsers, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})

			inbounds, ok := cfg["inbounds"].([]M)
			if !ok || len(inbounds) != 1 {
				t.Fatalf("inbounds = %#v, want only the node inbound", cfg["inbounds"])
			}

			if nc.TLS == 2 {
				settings := guardRealitySettings(t, inbounds[0])
				if settings["dest"] != "www.example.com:8443" {
					t.Fatalf("realitySettings.dest = %#v, want the original camouflage target", settings["dest"])
				}
			}

			routing := cfg["routing"].(M)
			for _, rule := range routing["rules"].([]M) {
				if tags, ok := rule["inboundTag"].([]string); ok {
					for _, tag := range tags {
						if tag == realityGuardTag {
							t.Fatalf("found guard routing rule %#v, want none", rule)
						}
					}
				}
			}
		})
	}
}

func TestBuildRouting_GuardRulesComeFirst(t *testing.T) {
	guardRules := []M{
		{"type": "field", "inboundTag": []string{realityGuardTag}, "domain": []string{"full:www.example.com"}, "outboundTag": "direct"},
		{"type": "field", "inboundTag": []string{realityGuardTag}, "outboundTag": "block"},
	}
	raw := []map[string]any{{"type": "field", "domain": []string{"geosite:cn"}, "outboundTag": "direct"}}
	relay := []M{{"type": "field", "inboundTag": []string{"relay-in"}, "outboundTag": "relay-out"}}

	routing := buildRouting(nil, nil, raw, relay, guardRules)
	rules := routing["rules"].([]M)
	if len(rules) < 2 {
		t.Fatalf("rules = %#v, want guard rules plus the rest", rules)
	}
	for i := 0; i < 2; i++ {
		tags, ok := rules[i]["inboundTag"].([]string)
		if !ok || len(tags) != 1 || tags[0] != realityGuardTag {
			t.Fatalf("rules[%d] = %#v, want the guard rule", i, rules[i])
		}
	}
}

func TestRealityGuardEnabled(t *testing.T) {
	cases := []struct {
		name  string
		tls   int
		value any
		want  bool
	}{
		{name: "布尔真", tls: 2, value: true, want: true},
		{name: "布尔假", tls: 2, value: false, want: false},
		{name: "字符串真", tls: 2, value: "true", want: true},
		{name: "字符串1", tls: 2, value: " 1 ", want: true},
		{name: "字符串假", tls: 2, value: "false", want: false},
		{name: "字符串非法", tls: 2, value: "yes please", want: false},
		{name: "浮点1", tls: 2, value: float64(1), want: true},
		{name: "浮点0", tls: 2, value: float64(0), want: false},
		{name: "整数1", tls: 2, value: 1, want: true},
		{name: "未下发", tls: 2, value: nil, want: false},
		{name: "非REALITY", tls: 1, value: true, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := map[string]any{}
			if tc.value != nil {
				settings["anti_abuse"] = tc.value
			}
			nc := &model.NodeSpec{TLS: tc.tls, TLSSettings: settings}
			if got := realityGuardEnabled(nc); got != tc.want {
				t.Fatalf("realityGuardEnabled() = %v, want %v", got, tc.want)
			}
		})
	}

	if realityGuardEnabled(&model.NodeSpec{TLS: 2}) {
		t.Fatal("realityGuardEnabled() = true for a node without TLS settings, want false")
	}
}

func TestRealityGuardDomains(t *testing.T) {
	got := realityGuardDomains([]string{" WWW.Example.com ", "www.example.com", "", "cdn.example.net"})
	want := []string{"full:www.example.com", "full:cdn.example.net"}
	if len(got) != len(want) {
		t.Fatalf("realityGuardDomains() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("realityGuardDomains()[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if domains := realityGuardDomains("www.example.com"); domains != nil {
		t.Fatalf("realityGuardDomains(string) = %#v, want nil", domains)
	}
	if domains := realityGuardDomains([]string{"  "}); len(domains) != 0 {
		t.Fatalf("realityGuardDomains(blank) = %#v, want empty", domains)
	}
}

func TestRealityDestPort(t *testing.T) {
	cases := []struct {
		name string
		dest any
		want int
	}{
		{name: "正常端口", dest: "www.example.com:8443", want: 8443},
		{name: "IPv6", dest: "[2001:db8::1]:8443", want: 8443},
		{name: "缺端口", dest: "www.example.com", want: realityGuardDefaultPort},
		{name: "端口越界", dest: "www.example.com:70000", want: realityGuardDefaultPort},
		{name: "非字符串", dest: 443, want: realityGuardDefaultPort},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := realityDestPort(tc.dest); got != tc.want {
				t.Fatalf("realityDestPort(%#v) = %d, want %d", tc.dest, got, tc.want)
			}
		})
	}
}

func TestAllocateLoopbackPort_SkipsOccupiedAndReserved(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法在本机监听，跳过：%v", err)
	}
	defer listener.Close()

	occupied := listener.Addr().(*net.TCPAddr).Port
	if occupied < realityGuardPortBase || occupied >= realityGuardPortBase+realityGuardPortSpan {
		t.Skipf("系统分配的端口 %d 不在防盗用端口段内，跳过", occupied)
	}

	got, err := allocateLoopbackPort(occupied, occupied+1)
	if err != nil {
		t.Fatalf("allocateLoopbackPort() error = %v", err)
	}
	if got == occupied {
		t.Fatalf("allocateLoopbackPort() = %d, must skip the occupied port", got)
	}
	if got == occupied+1 {
		t.Fatalf("allocateLoopbackPort() = %d, must skip the reserved port", got)
	}
}

func TestPreferredGuardPort(t *testing.T) {
	if got := preferredGuardPort(443); got != realityGuardPortBase+443 {
		t.Fatalf("preferredGuardPort(443) = %d, want %d", got, realityGuardPortBase+443)
	}
	// 同一节点端口必须落在同一个起点，方便排查。
	if preferredGuardPort(443) != preferredGuardPort(443) {
		t.Fatal("preferredGuardPort() is not stable for the same node port")
	}
	got := preferredGuardPort(-443)
	if got < realityGuardPortBase || got >= realityGuardPortBase+realityGuardPortSpan {
		t.Fatalf("preferredGuardPort(-443) = %d, want inside the guard port range", got)
	}
}
