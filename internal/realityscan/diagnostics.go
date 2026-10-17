package realityscan

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CDNResult struct {
	Status    string   `json:"status"`
	Providers []string `json:"providers"`
	Evidence  []string `json:"evidence"`
}

type HTTPResult struct {
	Status     string `json:"status"`
	StatusCode int    `json:"status_code,omitempty"`
}

// 只使用可归属厂商的特征；普通缓存头、DNS 托管商和证书签发者不能证明使用 CDN。
var cdnRules = []struct {
	provider string
	suffixes []string
	headers  []string
}{
	{"Cloudflare", []string{"cdn.cloudflare.net"}, []string{"Cf-Ray"}},
	{"Amazon CloudFront", []string{"cloudfront.net"}, []string{"X-Amz-Cf-Id", "X-Amz-Cf-Pop"}},
	{"Akamai", []string{"akamaiedge.net", "edgesuite.net", "edgekey.net", "akamaized.net"}, []string{"Akamai-Grn", "X-Akamai-Request-ID"}},
	{"Fastly", []string{"fastly.net", "fastlylb.net"}, []string{"Fastly-Debug-Digest", "X-Fastly-Request-ID"}},
	{"Bunny CDN", []string{"b-cdn.net", "bunnycdn.com"}, []string{"Cdn-PullZone", "Cdn-RequestId"}},
	{"阿里云 CDN", []string{"kunlunsl.com", "kunlunca.com", "kunlungr.com", "alikunlun.com"}, nil},
	{"腾讯云 CDN", []string{"cdn.dnsv1.com", "cdntip.com", "tcdn.qq.com"}, []string{"X-Nws-Log-Uuid"}},
	{"Azure Front Door / CDN", []string{"azureedge.net", "azurefd.net"}, []string{"X-Azure-Ref"}},
	{"Imperva", []string{"incapdns.net"}, []string{"X-Iinfo"}},
	{"Vercel", []string{"vercel-dns.com", "vercel-dns.net"}, []string{"X-Vercel-Id"}},
	{"Netlify", []string{"netlify.app", "netlify.global"}, []string{"X-Nf-Request-Id"}},
}

func identifyCDN(cname string, headers http.Header, complete bool) *CDNResult {
	result := &CDNResult{Status: "unknown", Providers: []string{}, Evidence: []string{}}
	if complete {
		result.Status = "not_detected"
	}
	cname = strings.TrimSuffix(strings.ToLower(cname), ".")
	for _, rule := range cdnRules {
		matched := false
		for _, suffix := range rule.suffixes {
			if cname == suffix || strings.HasSuffix(cname, "."+suffix) {
				result.Evidence = append(result.Evidence, "CNAME: "+cname)
				matched = true
				break
			}
		}
		for _, header := range rule.headers {
			if headers.Get(header) != "" {
				// 不上报响应头值，避免携带跟踪标识或站点数据。
				result.Evidence = append(result.Evidence, "HTTP: "+header)
				matched = true
				break
			}
		}
		server := strings.ToLower(strings.TrimSpace(headers.Get("Server")))
		if (rule.provider == "Cloudflare" && server == "cloudflare") ||
			(rule.provider == "Akamai" && server == "akamaighost") ||
			(rule.provider == "Bunny CDN" && strings.HasPrefix(server, "bunnycdn-")) {
			result.Evidence = append(result.Evidence, "HTTP: Server ("+rule.provider+")")
			matched = true
		}
		if matched {
			result.Providers = append(result.Providers, rule.provider)
			result.Status = "detected"
		}
	}
	return result
}

func (s Scanner) inspect(ctx context.Context, item target, result *Result, conn *tls.Conn) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var cname string
	var cnameOK bool
	var headers http.Header
	var wg sync.WaitGroup
	// 指定 IP + SNI 时不把 SNI 的 DNS 结果当作该 IP 的 CDN 证据。
	if net.ParseIP(item.host) == nil {
		wg.Go(func() {
			lookup := s.LookupCNAME
			if lookup == nil {
				lookup = net.DefaultResolver.LookupCNAME
			}
			value, err := lookup(ctx, item.host)
			if err == nil && validName(strings.TrimSuffix(strings.ToLower(value), ".")) {
				cname, cnameOK = value, true
			}
		})
	}
	if result.CertValid {
		result.HTTP, headers = readPageStatus(ctx, item, conn)
	}
	wg.Wait()
	result.CDN = identifyCDN(cname, headers, cnameOK && result.HTTP.Status == "received")
}

// 复用已经校验的 TLS 连接，页面、IP 和握手结果属于同一目标；不跟随跳转或读取正文。
func readPageStatus(ctx context.Context, item target, conn *tls.Conn) (*HTTPResult, http.Header) {
	transport := &http.Transport{
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 32 * 1024,
		ResponseHeaderTimeout:  3 * time.Second,
	}
	var used bool
	transport.DialTLSContext = func(context.Context, string, string) (net.Conn, error) {
		if used {
			return nil, errors.New("connection already used")
		}
		used = true
		return conn, nil
	}
	defer transport.CloseIdleConnections()
	address := (&url.URL{Scheme: "https", Host: net.JoinHostPort(item.sni, strconv.Itoa(item.port)), Path: "/"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return &HTTPResult{Status: "failed"}, nil
	}
	req.Header.Set("User-Agent", "YZ-Reality-Scanner/1.0")
	response, err := transport.RoundTrip(req)
	if err != nil {
		var networkError net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			return &HTTPResult{Status: "timeout"}, nil
		}
		return &HTTPResult{Status: "failed"}, nil
	}
	defer response.Body.Close()
	return &HTTPResult{Status: "received", StatusCode: response.StatusCode}, response.Header
}
