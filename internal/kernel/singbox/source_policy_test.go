package singbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
)

func TestSourcePolicyPrecedesCustomRoutesAndRejectsDirectly(t *testing.T) {
	file := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(file, []byte(`{"route":{"rules":[{"outbound":"direct"}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	n := &model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: 12345,
		SourcePolicy: &sourcepolicy.Policy{BlockCN: true}, SourcePolicyReady: true,
		SourceBlockCIDRs: []string{"198.51.100.0/24", "2001:db8::/32"}}
	cfg, err := buildConfig(config.KernelConfig{Type: "singbox", CustomConfig: file}, n, nil, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	rule := cfg["route"].(M)["rules"].([]M)[0]
	if rule["action"] != "reject" || rule["source_ip_cidr"] == nil || rule["ip_cidr"] != nil {
		t.Fatal("来源限制没有排在自定义路由之前")
	}
	if rule["inbound"].([]string)[0] != "vless-in" {
		t.Fatal("未限定实际代理入口")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := singJSON.UnmarshalExtendedContext[option.Options](include.Context(context.Background()), data); err != nil {
		t.Fatal(err)
	}
	n.SourcePolicyReady = false
	if _, err := buildConfig(config.KernelConfig{Type: "singbox"}, n, nil, kernel.TLSCert{}); err == nil {
		t.Fatal("未准备的策略应阻止启动")
	}
}
