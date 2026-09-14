package agentcli

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func kernelInitArgs(path string, extra ...string) []string {
	args := []string{
		"--mode", "node", "--panel-url", "https://kernel.example.invalid",
		"--node-id", "1", "--config", path, "--output", path,
		"--node-type", "shadowsocks",
		"--install-root", filepath.Join(filepath.Dir(path), "runtime"),
	}
	return append(args, extra...)
}

func loadKernelInstances(t *testing.T, path string) []*config.Config {
	t.Helper()
	raw, err := loadWritableRootConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range normalizeRootInstances(raw) {
		if inst.Panel.TokenEnv != "" {
			t.Setenv(inst.Panel.TokenEnv, rand.Text())
		}
		if inst.Machine != nil && inst.Machine.TokenEnv != "" {
			t.Setenv(inst.Machine.TokenEnv, rand.Text())
		}
	}
	root, err := config.LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	instances, err := root.NormalizeInstances()
	if err != nil {
		t.Fatal(err)
	}
	return instances
}

func TestConfigInitNewKernelDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, "singbox"},
		{"shadowsocks", []string{"--node-type", "shadowsocks"}, "singbox"},
		{"vmess", []string{"--node-type", "vmess"}, "singbox"},
		{"vless", []string{"--node-type", "vless"}, "xray"},
		{"vless_uppercase", []string{"--node-type", "VLESS"}, "xray"},
		{"machine", []string{"--mode", "machine", "--machine-id", "2"}, "singbox"},
		{"explicit_xray", []string{"--kernel", "xray"}, "xray"},
		{"explicit_vless_singbox", []string{"--node-type", "vless", "--kernel", "sing-box"}, "singbox"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			for range 2 {
				if err := runConfigInit(kernelInitArgs(path, tc.args...)); err != nil {
					t.Fatal(err)
				}
				instances := loadKernelInstances(t, path)
				if len(instances) != 1 || instances[0].Kernel.Type != tc.want {
					t.Fatalf("内核未按新绑定规则保存，期望 %s", tc.want)
				}
			}
		})
	}
}

func TestConfigInitDetectsNewNodeKernelFromPanel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol string
		kernel   string
		status   int
		want     string
	}{
		{"vless", "vless", "", http.StatusOK, "xray"},
		{"vmess", "vmess", "", http.StatusOK, "singbox"},
		{"panel_singbox", "vless", "sing-box", http.StatusOK, "singbox"},
		{"panel_xray", "shadowsocks", "xray", http.StatusOK, "xray"},
		{"missing_protocol", "", "", http.StatusOK, ""},
		{"blank_protocol", " ", "", http.StatusOK, ""},
		{"unknown_kernel", "vless", "unknown", http.StatusOK, ""},
		{"unavailable", "vless", "", http.StatusServiceUnavailable, ""},
		{"empty_snapshot", "vless", "", http.StatusNotModified, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential := rand.Text()
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/v1/server/UniProxy/config" || r.URL.Query().Get("token") != credential || r.URL.Query().Get("node_id") != "1" {
					t.Error("节点发现请求与安装参数不一致")
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"protocol": tc.protocol, "kernel_type": tc.kernel})
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "config.yml")
			args := kernelInitArgs(path, "--node-type", "", "--panel-url", server.URL, "--token", credential)
			err := runConfigInit(args)
			if requests.Load() != 1 {
				t.Fatal("新节点没有读取面板配置")
			}
			if tc.want == "" {
				if err == nil {
					t.Fatal("无法确认内核时应停止创建配置")
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatal("失败时仍写入了配置")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			first := loadKernelInstances(t, path)[0]
			if first.Kernel.Type != tc.want || first.Panel.NodeType != tc.protocol {
				t.Fatal("保存的协议或内核不符合面板配置")
			}
			// 重复绑定保留内核，即使此时面板已经无法连接。
			server.Close()
			if err := runConfigInit(args); err != nil {
				t.Fatal(err)
			}
			repeated := loadKernelInstances(t, path)[0]
			if repeated.Kernel.Type != tc.want || repeated.Panel.NodeType != tc.protocol || requests.Load() != 1 {
				t.Fatal("重复绑定重新选择了内核")
			}
		})
	}
}

func TestConfigInitExplicitKernelDoesNotRequirePanelDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := runConfigInit(kernelInitArgs(path, "--node-type", "", "--kernel", "xray")); err != nil {
		t.Fatal(err)
	}
	if loadKernelInstances(t, path)[0].Kernel.Type != "xray" {
		t.Fatal("显式内核选择未生效")
	}
}

func TestConfigInitMissingProtocolAndCredentialsDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := runConfigInit(kernelInitArgs(path, "--node-type", "")); err == nil {
		t.Fatal("缺少协议与凭据时不应猜测默认内核")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("缺少必要信息时仍写入了配置")
	}
}

func TestConfigInitPreservesExistingKernel(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"legacy_implicit", "panel:\n  url: https://kernel.example.invalid\n  node_id: 1\n  token_env: KERNEL_DEFAULT_TEST_TOKEN\n"},
		{"legacy_xray", "kernel:\n  type: xray\n  log_level: debug\npanel:\n  url: https://kernel.example.invalid\n  node_id: 1\n  token_env: KERNEL_DEFAULT_TEST_TOKEN\n"},
		{"legacy_singbox", "kernel:\n  type: singbox\n  log_level: debug\npanel:\n  url: https://kernel.example.invalid\n  node_id: 1\n  token_env: KERNEL_DEFAULT_TEST_TOKEN\n"},
		{"inherited_xray", "kernel:\n  type: xray\ninstances:\n  - panel:\n      url: https://kernel.example.invalid\n      node_id: 1\n      token_env: KERNEL_DEFAULT_TEST_TOKEN\n"},
		{"inherited_singbox", "kernel:\n  type: singbox\ninstances:\n  - panel:\n      url: https://kernel.example.invalid\n      node_id: 1\n      token_env: KERNEL_DEFAULT_TEST_TOKEN\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			before := loadKernelInstances(t, path)[0].Kernel
			for range 2 {
				if err := runConfigInit(kernelInitArgs(path, "--node-type", "vless")); err != nil {
					t.Fatal(err)
				}
				after := loadKernelInstances(t, path)[0].Kernel
				if !reflect.DeepEqual(before, after) {
					t.Fatal("重复安装改变了已有内核配置")
				}
			}
			if err := runConfigInit(kernelInitArgs(path, "--node-id", "2")); err != nil {
				t.Fatal(err)
			}
			instances := loadKernelInstances(t, path)
			if len(instances) != 2 || instances[0].Kernel.Type != before.Type || instances[1].Kernel.Type != "singbox" {
				t.Fatal("新增绑定没有独立选择默认内核，或改变了已有绑定")
			}
		})
	}
}

func TestConfigInitExplicitKernelCanChangeExistingBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := runConfigInit(kernelInitArgs(path)); err != nil {
		t.Fatal(err)
	}
	if err := runConfigInit(kernelInitArgs(path, "--kernel", "xray")); err != nil {
		t.Fatal(err)
	}
	if got := loadKernelInstances(t, path)[0].Kernel.Type; got != "xray" {
		t.Fatalf("显式选择内核得到 %q，期望 xray", got)
	}
}

func TestConfigInitKernelErrorsDoNotOverwriteExistingFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		args []string
	}{
		{"invalid_config", "instances: [\n", nil},
		{"invalid_kernel", "kernel:\n  type: xray\n", []string{"--kernel", "unknown"}},
		{"empty_kernel", "kernel:\n  type: xray\n", []string{"--kernel", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runConfigInit(kernelInitArgs(path, tc.args...)); err == nil {
				t.Fatal("无效配置应返回错误")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != tc.yaml {
				t.Fatal("失败时覆盖了已有配置")
			}
		})
	}
}
