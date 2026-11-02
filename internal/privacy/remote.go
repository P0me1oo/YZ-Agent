package privacy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Bootstrap struct {
	URL             string `json:"url"`
	MachineID       int    `json:"machine_id,omitempty"`
	NodeID          int    `json:"node_id,omitempty"`
	TokenEnv        string `json:"token_env,omitempty"`
	Token           string `json:"token,omitempty"`
	CredentialsFile string `json:"credentials_file,omitempty"`
}

func (b Bootstrap) Credential(configPath string) string {
	if b.MachineID > 0 && os.Getenv("MACHINE_TOKEN") != "" {
		return os.Getenv("MACHINE_TOKEN")
	}
	if b.MachineID == 0 && os.Getenv("PANEL_TOKEN") != "" {
		return os.Getenv("PANEL_TOKEN")
	}
	if b.Token != "" {
		return b.Token
	}
	if b.TokenEnv != "" {
		if value := os.Getenv(b.TokenEnv); value != "" {
			return value
		}
		paths := []string{filepath.Join(filepath.Dir(configPath), "credentials.env"), b.CredentialsFile}
		if before, _, ok := strings.Cut(filepath.ToSlash(configPath), "/backups/"); ok {
			paths = append(paths, filepath.Join(before, "credentials.env"))
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
				if ok && key == b.TokenEnv {
					return value
				}
			}
		}
	}
	return b.Token
}

// 密钥接口拒绝跨站跳转；错误中不包含认证参数、响应正文或密钥。
func Fetch(ctx context.Context, b Bootstrap, configPath string, client *http.Client) (*State, bool, error) {
	if override := os.Getenv("PANEL_URL"); override != "" {
		b.URL = override
	}
	u, err := url.Parse(b.URL)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return nil, false, errors.New("文件保护的面板入口无效")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	secure := u.Scheme == "https" || u.Scheme == "http" && local
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, false, errors.New("面板入口协议无效")
	}
	token := b.Credential(configPath)
	if token == "" {
		return nil, false, errors.New("无法取得文件保护所需的机器认证凭据")
	}
	auth := map[string]any{"token": token}
	if !secure {
		auth["probe"] = true
	}
	endpoint := "/api/v2/server/privacy"
	if b.MachineID > 0 {
		auth["machine_id"] = b.MachineID
		endpoint = "/api/v2/server/machine/privacy"
	} else {
		auth["node_id"] = b.NodeID
	}
	body, _ := json.Marshal(auth)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(b.URL, "/")+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, false, errors.New("无法创建文件保护请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		return nil, false, errors.New("暂时无法向面板领取文件密钥，稍后自动重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return legacyPolicy(ctx, &copyClient, b, body)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, errors.New("面板未授权文件密钥请求，保留原文件")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return nil, false, errors.New("文件保护响应无效")
	}
	var result struct {
		Data struct {
			Version int   `json:"version"`
			Enabled bool  `json:"enabled"`
			Keys    []Key `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Data.Version == 0 {
		return legacyPolicy(ctx, &copyClient, b, body)
	}
	if result.Data.Version != 1 {
		return nil, false, errors.New("面板不支持所需的文件保护版本")
	}
	if !result.Data.Enabled {
		return nil, false, nil
	}
	if !secure {
		return nil, false, errors.New("文件保护需要 HTTPS 面板入口")
	}
	s, err := New(result.Data.Keys)
	return s, true, err
}

// 旧面板没有密钥接口时，已开启的隐私策略不能静默降级为明文。
func legacyPolicy(ctx context.Context, client *http.Client, b Bootstrap, body []byte) (*State, bool, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(b.URL, "/")+"/api/v2/server/handshake", bytes.NewReader(body))
	if err != nil {
		return nil, false, errors.New("无法核对旧面板的隐私策略")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, errors.New("暂时无法核对面板的隐私策略")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, errors.New("面板未返回有效的隐私策略")
	}
	var result struct {
		Policy *struct {
			Disabled bool `json:"disabled"`
		} `json:"log_policy"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&result) != nil {
		return nil, false, errors.New("旧面板隐私策略格式无效")
	}
	if result.Policy != nil && result.Policy.Disabled {
		return nil, false, errors.New("隐私保护需要先更新面板，不能退回明文保存")
	}
	return nil, false, nil
}
