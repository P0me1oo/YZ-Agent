// Package healthcheck 检查本机监听与真实核心出站；不创建面板用户、不进入用户计费。
package healthcheck

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/xhttp"
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
func Target(ctx context.Context, raw string) (*url.URL, []string, error) {
	return resolveTarget(ctx, raw, net.DefaultResolver.LookupIPAddr)
}

func resolveTarget(
	ctx context.Context,
	raw string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
) (*url.URL, []string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("只支持无认证信息的 HTTPS 公网地址")
	}
	invalidEndpoint := u.Scheme != "https" || u.Hostname() == ""
	forbiddenParts := u.User != nil || u.Fragment != "" || u.RawQuery != ""
	if invalidEndpoint || forbiddenParts {
		return nil, nil, fmt.Errorf("只支持无认证信息的 HTTPS 公网地址")
	}
	ips, err := lookup(ctx, u.Hostname())
	if err != nil {
		return nil, nil, fmt.Errorf("目标域名解析失败")
	}
	if len(ips) == 0 {
		return nil, nil, fmt.Errorf("目标没有地址")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	families := [2][]string{{}, {}}
	seen := make(map[string]bool, len(ips))
	firstIPv4 := ips[0].IP.To4() != nil
	for _, resolved := range ips {
		ip := resolved.IP.String()
		// 所有答案都必须通过校验，不能因只尝试部分地址而放过非公网答案。
		if deviceip.PublicAddress(ip) == "" {
			return nil, nil, fmt.Errorf("目标不是公网地址")
		}
		if seen[ip] {
			continue
		}
		seen[ip] = true
		family := 0
		if (resolved.IP.To4() != nil) != firstIPv4 {
			family = 1
		}
		families[family] = append(families[family], net.JoinHostPort(ip, port))
	}
	// 前置的地址优先级不代表落地的连通性；尽早尝试另一地址族。
	addresses := make([]string, 0, len(seen))
	for i := range max(len(families[0]), len(families[1])) {
		for _, family := range families {
			if i < len(family) {
				addresses = append(addresses, family[i])
			}
		}
	}
	return u, addresses, nil
}

func Request(ctx context.Context, u *url.URL, addresses []string, dial DialFunc) (int64, error) {
	transport := &http.Transport{
		TLSClientConfig:       &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
		DialContext:           dial,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	return request(
		ctx,
		u,
		addresses,
		transport,
	)
}

func request(ctx context.Context, u *url.URL, addresses []string, base *http.Transport) (int64, error) {
	started := time.Now()
	if len(addresses) == 0 {
		return 0, fmt.Errorf("目标没有地址")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	var lastErr error
	for i, address := range addresses {
		if err := ctx.Err(); err != nil {
			return time.Since(started).Milliseconds(), err
		}
		timeout := time.Until(deadline)
		if i < len(addresses)-1 {
			// 共享八秒预算，避免首个无响应地址耗尽后续回退的时间。
			timeout /= 2
		}
		attempt, cancelAttempt := context.WithTimeout(ctx, timeout)
		retryable, err := requestAddress(
			attempt,
			u,
			address,
			base,
		)
		cancelAttempt()
		if err == nil || !retryable {
			return time.Since(started).Milliseconds(), err
		}
		lastErr = err
	}
	return time.Since(started).Milliseconds(), lastErr
}

func requestAddress(ctx context.Context, u *url.URL, address string, base *http.Transport) (bool, error) {
	transport := base.Clone()
	transport.DialContext = func(ctx context.Context, network, ignored string) (net.Conn, error) {
		return base.DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		u.String(),
		nil,
	)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", "YZ-Agent-Health/1")
	resp, err := client.Do(req)
	if err != nil {
		return true, fmt.Errorf("代理出站或 TLS 请求失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("目标返回 HTTP %d", resp.StatusCode)
	}
	return false, nil
}
func IsTCP(protocol, network string, tls int, settings map[string]any) bool {
	return !xhttp.IsH3(network, tls, settings) && !strings.Contains(protocol, "hysteria") && protocol != "tuic" && protocol != "wireguard" && network != "kcp" && network != "hysteria"
}
