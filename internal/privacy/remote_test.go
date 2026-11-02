package privacy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteKeyUsesAuthenticatedPostAndNeverFollowsRedirects(t *testing.T) {
	token := rand.Text()
	key := testKey(t)
	redirect := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.RawQuery != "" {
			t.Error("认证凭据不得放入查询参数")
		}
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload["token"] != token || payload["machine_id"] != float64(7) {
			w.WriteHeader(403)
			return
		}
		if redirect {
			w.Header().Set("Location", "https://example.invalid/")
			w.WriteHeader(307)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "enabled": true, "keys": []Key{key}}})
	}))
	defer server.Close()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(filepath.Join(dir, "credentials.env"), []byte("TEST_PRIVATE_AUTH="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := Bootstrap{URL: server.URL, MachineID: 7, TokenEnv: "TEST_PRIVATE_AUTH"}
	state, enabled, err := Fetch(context.Background(), b, configPath, nil)
	if err != nil || !enabled || state == nil {
		t.Fatal("无法自动领取密钥")
	}
	redirect = true
	_, _, err = Fetch(context.Background(), b, configPath, nil)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), key.Value) {
		t.Fatal("跳转或错误内容泄露了认证信息")
	}
}

type privacyTransport func(*http.Request) (*http.Response, error)

func (f privacyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRemoteInsecureHTTPOnlyProbesAndOldPanelCannotDisableProtection(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "private"}[enabled], func(t *testing.T) {
			client := &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
				var input map[string]any
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input["probe"] != true {
					t.Error("普通 HTTP 不得请求文件密钥")
				}
				body, _ := json.Marshal(map[string]any{"data": map[string]any{"version": 1, "enabled": enabled, "keys": []Key{}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
			})}
			_, active, err := Fetch(context.Background(), Bootstrap{URL: "http://panel.example.invalid", MachineID: 1, Token: rand.Text()}, "", client)
			if active || (err != nil) != enabled {
				t.Fatal("HTTPS 限制没有按模式生效")
			}
		})
	}
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-normal", true: "legacy-private"}[disabled], func(t *testing.T) {
			client := &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
				status, body := 404, "{}"
				if r.URL.Path == "/api/v2/server/handshake" {
					status = 200
					encoded, _ := json.Marshal(map[string]any{"log_policy": map[string]any{"disabled": disabled}})
					body = string(encoded)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})}
			_, active, err := Fetch(context.Background(), Bootstrap{URL: "https://panel.example.invalid", MachineID: 1, Token: rand.Text()}, "", client)
			if active || (err != nil) != disabled {
				t.Fatal("旧面板已开启保护时必须等待升级，不能保存明文")
			}
		})
	}
}
