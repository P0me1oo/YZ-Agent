// Package realityscan 检查公网伪装目标，并从指定地址的证书发现候选域名。
package realityscan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const MaxTargets = 512

// 默认候选不包含 Cloudflare；管理员仍可主动输入该域名。
var Defaults = []string{
	"www.microsoft.com:443", "www.amazon.com:443", "aws.amazon.com:443",
	"www.samsung.com:443", "www.nvidia.com:443", "www.amd.com:443",
	"www.intel.com:443", "www.sony.com:443", "dl.google.com:443",
}

type Result struct {
	Target         string      `json:"target"`
	Host           string      `json:"host"`
	IP             string      `json:"ip"`
	DiscoveredIP   string      `json:"discovered_ip,omitempty"`
	Port           int         `json:"port"`
	Feasible       bool        `json:"feasible"`
	TLS13          bool        `json:"tls13"`
	TLSVersion     string      `json:"tls_version"`
	H2             bool        `json:"h2"`
	ALPN           string      `json:"alpn"`
	X25519         bool        `json:"x25519"`
	Curve          string      `json:"curve"`
	CertValid      bool        `json:"cert_valid"`
	CertChainValid bool        `json:"cert_chain_valid"`
	CertChainBytes int         `json:"cert_chain_bytes"`
	CertSubject    string      `json:"cert_subject"`
	CertIssuer     string      `json:"cert_issuer"`
	NotAfter       string      `json:"not_after"`
	ServerNames    []string    `json:"server_names"`
	LatencyMS      int64       `json:"latency_ms"`
	Reason         string      `json:"reason"`
	CDN            *CDNResult  `json:"cdn,omitempty"`
	HTTP           *HTTPResult `json:"http,omitempty"`
}

type target struct {
	host string
	port int
	sni  string
}

func ParseTarget(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	host, port := value, 443
	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
		port, err = strconv.Atoi(parsedPort)
		if err != nil || port < 1 || port > 65535 {
			return "", 0, errors.New("invalid_target")
		}
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		return ip.Unmap().String(), port, nil
	}
	if !validName(host) {
		return "", 0, errors.New("invalid_target")
	}
	return host, port, nil
}

func validName(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return false
			}
		}
	}
	return true
}

func expand(values []string, sni string) ([]target, error) {
	if len(values) == 0 {
		values = Defaults
	}
	if len(values) > 64 || (sni != "" && (len(values) != 1 || !validName(sni))) {
		return nil, errors.New("invalid_target")
	}
	var targets []target
	seen := map[string]bool{}
	appendTarget := func(host string, port int, name string) {
		key := net.JoinHostPort(host, strconv.Itoa(port))
		if !seen[key] {
			seen[key] = true
			targets = append(targets, target{host, port, name})
		}
	}
	for _, value := range values {
		if strings.Contains(value, "/") {
			prefix, err := netip.ParsePrefix(value)
			if err != nil || prefix.Addr().Is4In6() || prefix.Addr().BitLen()-prefix.Bits() > 8 || sni != "" {
				return nil, errors.New("invalid_cidr")
			}
			prefix = prefix.Masked()
			for ip := prefix.Addr(); prefix.Contains(ip); ip = ip.Next() {
				appendTarget(ip.String(), 443, "")
				if len(targets) > MaxTargets {
					return nil, errors.New("too_many_targets")
				}
			}
		} else {
			host, port, err := ParseTarget(value)
			if err != nil {
				return nil, err
			}
			name := sni
			if name == "" && net.ParseIP(host) == nil {
				name = host
			}
			appendTarget(host, port, name)
		}
		if len(targets) > MaxTargets {
			return nil, errors.New("too_many_targets")
		}
	}
	return targets, nil
}

// 公网地址之外还排除文档、基准测试及过渡地址，避免绕过私网限制。
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range reserved {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type Scanner struct {
	// 测试替身仍须经过公网地址检查，实际连接只使用已经核对的 IP。
	Lookup      func(context.Context, string) ([]netip.Addr, error)
	Dial        func(context.Context, string, string) (net.Conn, error)
	Roots       *x509.CertPool
	Diagnostics bool
	LookupCNAME func(context.Context, string) (string, error)
}

func certificateLabel(value string) string {
	runes := []rune(strings.ToValidUTF8(value, ""))
	return string(runes[:min(len(runes), 512)])
}

func (s Scanner) dial(ctx context.Context, host string, port int) (net.Conn, string, error) {
	ips := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = append(ips, ip)
	} else {
		lookup := s.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, name string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", name)
			}
		}
		var err error
		ips, err = lookup(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, "", errors.New("dns_failed")
		}
	}
	for _, ip := range ips {
		if !publicAddress(ip) {
			return nil, "", errors.New("private_target")
		}
	}
	dial := s.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	for _, ip := range ips {
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), strconv.Itoa(port)))
		if err == nil {
			return conn, ip.Unmap().String(), nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, "", errors.New("connection_failed")
}

func (s Scanner) probe(ctx context.Context, item target) Result {
	r := Result{Host: item.sni, Port: item.port, Target: net.JoinHostPort(item.host, strconv.Itoa(item.port)), ServerNames: []string{}}
	if s.Diagnostics {
		r.CDN = &CDNResult{Status: "unknown", Providers: []string{}, Evidence: []string{}}
		r.HTTP = &HTTPResult{Status: "not_checked"}
	}
	timeout := 10 * time.Second
	if item.sni == "" {
		timeout = 4 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	conn, ip, err := s.dial(ctx, item.host, item.port)
	if err != nil {
		r.Reason = err.Error()
		return r
	}
	defer conn.Close()
	r.IP = ip
	// 名称和证书链在握手后分别验证，才能同时显示失败原因及证书域名。
	tlsConn := tls.Client(conn, &tls.Config{ServerName: item.sni, InsecureSkipVerify: true,
		MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"},
		CurvePreferences: []tls.CurveID{tls.X25519, tls.X25519MLKEM768}})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		r.Reason = "tls_failed"
		return r
	}
	r.LatencyMS = max(1, time.Since(started).Milliseconds())
	state := tlsConn.ConnectionState()
	r.TLS13 = state.Version == tls.VersionTLS13
	if r.TLS13 {
		r.TLSVersion = "1.3"
	} else {
		r.TLSVersion = "1.2"
	}
	r.ALPN, r.H2 = state.NegotiatedProtocol, state.NegotiatedProtocol == "h2"
	r.Curve = state.CurveID.String()
	r.X25519 = state.CurveID == tls.X25519 || state.CurveID == tls.X25519MLKEM768
	if len(state.PeerCertificates) == 0 {
		r.Reason = "no_certificate"
		return r
	}
	leaf := state.PeerCertificates[0]
	r.CertSubject = certificateLabel(leaf.Subject.CommonName)
	r.CertIssuer = certificateLabel(leaf.Issuer.CommonName)
	r.NotAfter = leaf.NotAfter.UTC().Format(time.RFC3339)
	for _, name := range leaf.DNSNames {
		name = strings.ToLower(name)
		if validName(name) && len(r.ServerNames) < 32 {
			r.ServerNames = append(r.ServerNames, name)
		}
	}
	opts := x509.VerifyOptions{Roots: s.Roots, Intermediates: x509.NewCertPool()}
	for i, cert := range state.PeerCertificates {
		r.CertChainBytes += len(cert.Raw)
		if i > 0 {
			opts.Intermediates.AddCert(cert)
		}
	}
	_, err = leaf.Verify(opts)
	r.CertChainValid = err == nil
	if item.sni == "" {
		// 证书域名只是候选，必须再按域名和 SNI 连接一次后才能回填。
		if len(r.ServerNames) == 0 {
			r.Reason = "no_domain"
			return r
		}
		r.Host = r.ServerNames[0]
		r.Reason = "discovered"
		return r
	}
	opts.DNSName = item.sni
	_, err = leaf.Verify(opts)
	r.CertValid = err == nil
	switch {
	case !r.CertValid:
		r.Reason = "certificate_invalid"
	case !r.TLS13:
		r.Reason = "tls13_required"
	case !r.H2:
		r.Reason = "h2_required"
	case !r.X25519:
		r.Reason = "x25519_required"
	default:
		r.Feasible = true
	}
	if s.Diagnostics {
		s.inspect(ctx, item, &r, tlsConn)
	}
	return r
}

func (s Scanner) Scan(ctx context.Context, values []string, sni string) ([]Result, error) {
	targets, err := expand(values, strings.TrimSuffix(strings.ToLower(strings.TrimSpace(sni)), "."))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 170*time.Second)
	defer cancel()
	results := make([]Result, len(targets))
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(32, len(targets)) {
		wg.Go(func() {
			for index := range work {
				item := targets[index]
				r := s.probe(ctx, item)
				if r.Reason == "discovered" && r.CertChainValid {
					verified := s.probe(ctx, target{r.Host, item.port, r.Host})
					verified.DiscoveredIP = r.IP
					r = verified
				}
				results[index] = r
			}
		})
	}
	for i := range targets {
		work <- i
	}
	close(work)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, errors.New("timeout")
	}
	best := map[string]Result{}
	for _, r := range results {
		previous, exists := best[r.Target]
		if !exists || (r.Feasible && !previous.Feasible) || (r.Feasible == previous.Feasible && r.LatencyMS > 0 && (previous.LatencyMS == 0 || r.LatencyMS < previous.LatencyMS)) {
			best[r.Target] = r
		}
	}
	out := make([]Result, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Feasible != b.Feasible {
			return a.Feasible
		}
		if (a.LatencyMS > 0) != (b.LatencyMS > 0) {
			return a.LatencyMS > 0
		}
		if a.LatencyMS != b.LatencyMS {
			return a.LatencyMS < b.LatencyMS
		}
		return a.Target < b.Target
	})
	return out, nil
}
