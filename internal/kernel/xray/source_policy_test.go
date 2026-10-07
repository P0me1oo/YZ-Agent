package xray

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	"github.com/xtls/xray-core/infra/conf/serial"
)

func TestSourcePolicyPrecedesCustomRoutesAndUsesDedicatedBlock(t *testing.T) {
	file := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(file, []byte(`{"routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"direct"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	kcfg := config.KernelConfig{Type: "xray", CustomConfig: file, CustomOutbound: []map[string]any{
		{"tag": "block", "protocol": "freedom"}, {"tag": "yz-source-block", "protocol": "freedom"},
	}}
	n := &model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: 12345,
		SourcePolicy: &sourcepolicy.Policy{BlockCN: true}, SourcePolicyReady: true,
		SourceBlockCIDRs: []string{"198.51.100.0/24", "2001:db8::/32"}}
	cfg := buildConfig(kcfg, n, nil, kernel.TLSCert{})
	rule := cfg["routing"].(M)["rules"].([]M)[0]
	if rule["outboundTag"] != "yz-source-block-1" || rule["source"] == nil || rule["ip"] != nil {
		t.Fatalf("来源限制被目标路由或自定义出口覆盖: %#v", rule)
	}
	if rule["inboundTag"].([]string)[0] != "vless-in" {
		t.Fatal("未限定实际代理入口")
	}
	outbounds := cfg["outbounds"].([]M)
	if outbounds[len(outbounds)-1]["protocol"] != "blackhole" {
		t.Fatal("没有创建独立丢弃出口")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serial.LoadJSONConfig(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	n.SourcePolicyReady = false
	if err := kernel.ValidateSourcePolicy(n); err == nil {
		t.Fatal("未准备的策略应阻止启动")
	}
}
