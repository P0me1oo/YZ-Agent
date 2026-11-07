// Package healthcheck 检查本机监听与真实核心出站；不创建面板用户、不进入用户计费。
package healthcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/xhttp"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Enabled  bool   `json:"enabled"`
	URL      string `json:"url"`
	Interval int    `json:"interval"`
	Request  string `json:"request,omitempty"`
}
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	MS     int64  `json:"latency_ms"`
	Error  string `json:"error,omitempty"`
}
type Result struct {
	ConfigHash string  `json:"config_hash"`
	ID         string  `json:"id"`
	Checked    int64   `json:"checked_at"`
	Status     string  `json:"status"`
	Target     string  `json:"target"`
	Source     string  `json:"source"`
	Steps      []Step  `json:"steps"`
	Routes     []Route `json:"routes,omitempty"`
}
type Route struct {
	Tag    string `json:"tag"`
	NodeID int    `json:"node_id,omitempty"`
	Status string `json:"status"`
	MS     int64  `json:"latency_ms"`
	Error  string `json:"error,omitempty"`
}
type DialFunc func(context.Context, string, string) (net.Conn, error)

// 目标仅允许 HTTPS 公网地址，使用核对后的 IP 建连，重定向不继续请求。
func Target(ctx context.Context, raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, "", fmt.Errorf("只支持无认证信息的 HTTPS 公网地址")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if err != nil {
		return nil, "", fmt.Errorf("目标域名解析失败")
	}
	if len(ips) == 0 {
		return nil, "", fmt.Errorf("目标没有地址")
	}
	for _, v := range ips {
		ip := v.IP
		if deviceip.PublicAddress(ip.String()) == "" {
			return nil, "", fmt.Errorf("目标不是公网地址")
		}
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return u, net.JoinHostPort(ips[0].IP.String(), port), nil
}
func Request(ctx context.Context, u *url.URL, address string, dial DialFunc) (int64, error) {
	started := time.Now()
	transport := &http.Transport{TLSClientConfig: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, ignored string) (net.Conn, error) {
		return dial(ctx, network, address)
	}, DisableKeepAlives: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "YZ-Agent-Health/1")
	resp, err := client.Do(req)
	if err != nil {
		return time.Since(started).Milliseconds(), fmt.Errorf("代理出站或 TLS 请求失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return time.Since(started).Milliseconds(), fmt.Errorf("目标返回 HTTP %d", resp.StatusCode)
	}
	return time.Since(started).Milliseconds(), nil
}
func IsTCP(protocol, network string, tls int, settings map[string]any) bool {
	return !xhttp.IsH3(network, tls, settings) && !strings.Contains(protocol, "hysteria") && protocol != "tuic" && protocol != "wireguard" && network != "kcp" && network != "hysteria"
}
