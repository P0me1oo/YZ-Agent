package privacy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteHTTPConfirmationIsBoundToEndpoint(t *testing.T) {
	const address = "http://panel.example.invalid:8080"
	key := testKey(t)
	for _, tc := range []struct {
		name, confirmed, override string
		allowed                   bool
	}{
		{"unconfirmed", "", "", false},
		{"confirmed", address, "", true},
		{"normalized", "HTTP://PANEL.EXAMPLE.INVALID:8080/", "", true},
		{"other_host", "http://other.example.invalid:8080", "", false},
		{"other_port", "http://panel.example.invalid:8081", "", false},
		{"https_is_not_http", "https://panel.example.invalid:8080", "", false},
		{"override", address, "http://other.example.invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PANEL_URL", tc.override)
			b := Bootstrap{URL: address, HTTPConfirmedURL: tc.confirmed, MachineID: 1, Token: rand.Text()}
			client := &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
				var input map[string]any
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if (input["probe"] == true) == tc.allowed {
					t.Fatal("未按入口确认状态决定是否领取密钥")
				}
				body, _ := json.Marshal(map[string]any{"data": map[string]any{
					"version": 1, "enabled": true, "keys": []Key{key},
				}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
			})}
			state, enabled, err := Fetch(t.Context(), b, "", client)
			if (err == nil && enabled && state != nil) != tc.allowed {
				t.Fatalf("确认状态未生效: %v", err)
			}
			if tc.allowed {
				b.HTTPConfirmedURL = ""
				if CachedAuthenticatedState(b, "") != nil {
					t.Fatal("撤回 HTTP 确认后仍可复用旧授权的密钥缓存")
				}
			}
		})
	}
}

func TestRemoteHTTPConfirmationKeepsTLSValidationAndRedirectProtection(t *testing.T) {
	key := testKey(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"version": 1, "enabled": true, "keys": []Key{key},
		}})
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	b := Bootstrap{URL: server.URL, HTTPConfirmedURL: server.URL, MachineID: 1, Token: rand.Text()}
	if _, _, err := Fetch(t.Context(), b, "", nil); !errors.Is(err, ErrPanelUnavailable) {
		t.Fatalf("自签名 HTTPS 不应被 HTTP 确认放行: %v", err)
	}
	if state, enabled, err := Fetch(t.Context(), b, "", server.Client()); err != nil || !enabled || state == nil {
		t.Fatalf("可信 HTTPS 应正常领取密钥: %v", err)
	}
	calls := 0
	client := &http.Client{Transport: privacyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: 307, Body: io.NopCloser(strings.NewReader("")),
			Header: http.Header{"Location": []string{"http://other.example.invalid/privacy"}},
		}, nil
	})}
	b.URL, b.HTTPConfirmedURL = "http://panel.example.invalid", "http://panel.example.invalid"
	if _, _, err := Fetch(t.Context(), b, "", client); err == nil || calls != 1 {
		t.Fatal("确认 HTTP 后不应把凭据转发到跳转地址")
	}
}

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
