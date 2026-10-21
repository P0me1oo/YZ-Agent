//go:build with_utls

package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// 本机只做内存内检查，不启动网络客户端或产生性能测试流量。
func TestGenerateRoleConfigurations(t *testing.T) {
	dir := t.TempDir()
	if err := generate(dir, "192.0.2.10", "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	front, err := load(filepath.Join(dir, "front.json"))
	if err != nil {
		t.Fatal(err)
	}
	landing, err := load(filepath.Join(dir, "landing.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := load(filepath.Join(dir, "client.json"))
	if err != nil {
		t.Fatal(err)
	}
	if front.PrivateKey == "" || len(landing.CertificateKey) == 0 || client.PrivateKey != "" || client.BaseUUID != "" || client.SSKey != "" || len(client.CertificateKey) != 0 {
		t.Fatal("角色配置的密钥范围错误")
	}
	if front.PublicKey != landing.PublicKey || client.PublicKey != front.PublicKey || front.ClientUUID[14:18] != "000c" || front.BaseUUID[14:18] != "0000" {
		t.Fatal("三组对照的客户端身份没有对齐")
	}
	certificate, err := x509.ParseCertificate(client.Certificate)
	if err != nil || certificate.VerifyHostname(serverName) != nil {
		t.Fatal("HTTPS 测试证书不匹配")
	}
	if err := generate(dir, "192.0.2.10", "192.0.2.20"); err == nil {
		t.Fatal("覆盖已有测试配置未被拒绝")
	}
	data, err := os.ReadFile(filepath.Join(dir, "realm.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), front.PrivateKey) || !strings.Contains(string(data), "28101") {
		t.Fatal("Realm 配置包含不需要的身份或端口错误")
	}
}

func TestRealCoreRolePaths(t *testing.T) {
	dir := t.TempDir()
	if err := generate(dir, "192.0.2.10", "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	c, err := load(filepath.Join(dir, "landing.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"new", "buffered", "realm", "old-ss", "old-vless"} {
		n, users := landingNode(c, mode)
		if err := model.ValidateNodeSpec(n, config.KernelConfig{Type: "singbox"}); err != nil {
			t.Fatalf("%s 落地: %v", mode, err)
		}
		if mode == "old-ss" || mode == "old-vless" {
			if len(users) != 0 || !n.IsRelayLanding() {
				t.Fatal("内部落地误用了普通用户身份")
			}
			front, users := frontNode(c, mode)
			if err := model.ValidateNodeSpec(front, config.KernelConfig{Type: "singbox"}); err != nil {
				t.Fatalf("%s 前置: %v", mode, err)
			}
			child := front.Relay.Children[0]
			if users[0].UUID != c.BaseUUID || !users[0].AllowsRelayRoute(12) || child.Address != c.Landing || child.Protocol != n.Protocol || child.Port != n.ServerPort {
				t.Fatal("旧中转入口的路径或身份不匹配")
			}
			if mode == "old-vless" && (child.VLESS.Flow != n.Flow || child.VLESS.ID != n.Relay.VLESS.ID || child.VLESS.RealitySettings["short_id"] != n.TLSSettings["short_id"]) {
				t.Fatal("VLESS 内部连接未使用相同 Reality/Vision 设置")
			}
			if mode == "old-ss" && child.Password != n.Relay.Password {
				t.Fatal("SS2022 内部身份未对齐")
			}
		} else if len(users) != 1 || users[0].UUID != c.ClientUUID || n.ServerPort != landingPort || n.Relay != nil {
			t.Fatal("透传落地未使用客户端身份")
		}
	}
}

func TestTimingSummary(t *testing.T) {
	if timingSummary(nil) != nil {
		t.Fatal("没有成功请求时不应生成延迟数据")
	}
	result := timingSummary([]float64{9, 1, 5, 3, 7})
	if result["min"] != 1 || result["p50"] != 5 || result["max"] != 9 {
		t.Fatal("延迟统计错误")
	}
}

func TestPayloadHandler(t *testing.T) {
	const token = "temporary-memory-only-token"
	h := payloadHandler(token)
	for _, tc := range []struct {
		path, method string
		auth         bool
		body         []byte
		status       int
		size         int
	}{
		{"/download?bytes=8192", "GET", true, nil, 200, 8192},
		{"/download?bytes=-1", "GET", true, nil, 400, 0},
		{"/download?bytes=536870913", "GET", true, nil, 400, 0},
		{"/download?bytes=1", "GET", false, nil, 404, 0},
		{"/upload", "POST", true, bytes.Repeat([]byte{1}, 16384), 200, 0},
	} {
		r := httptest.NewRequest(tc.method, tc.path, bytes.NewReader(tc.body))
		if tc.auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || (tc.size > 0 && w.Body.Len() != tc.size) {
			t.Fatal("HTTP 载荷长度或错误路径不符")
		}
		if tc.method == http.MethodPost && !strings.Contains(w.Body.String(), "16384") {
			t.Fatal("上传未返回实际接收量")
		}
	}
}

func TestClientBoundsBeforeNetworkInitialization(t *testing.T) {
	for _, tc := range []struct {
		direction             string
		bytes                 int64
		concurrency, requests int
	}{
		{"wrong", 1, 1, 1}, {"download", 0, 1, 1}, {"download", 513 << 20, 1, 1}, {"download", 10, 33, 33}, {"upload", 1, 2, 2},
	} {
		if err := runClient(context.Background(), perfConfig{}, "test", tc.direction, tc.bytes, tc.concurrency, tc.requests); err == nil {
			t.Fatal("越界测试参数未被拒绝")
		}
	}
}
