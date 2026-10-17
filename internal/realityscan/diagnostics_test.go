package realityscan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCDNRecognizesProvidersWithoutGenericCacheFalsePositives(t *testing.T) {
	for _, tc := range []struct {
		cname     string
		headers   http.Header
		providers []string
	}{
		{"D123.CLOUDFRONT.NET.", nil, []string{"Amazon CloudFront"}},
		{"a.edgesuite.net", nil, []string{"Akamai"}},
		{"a.fastly.net", nil, []string{"Fastly"}},
		{"a.kunlunsl.com", nil, []string{"阿里云 CDN"}},
		{"a.cdn.dnsv1.com", nil, []string{"腾讯云 CDN"}},
		{"a.azurefd.net", nil, []string{"Azure Front Door / CDN"}},
		{"example.com", http.Header{"Cf-Ray": {"test-only"}}, []string{"Cloudflare"}},
		{"example.com", http.Header{"Server": {"cloudflare"}}, []string{"Cloudflare"}},
		{"example.com", http.Header{"Server": {"BunnyCDN-LA1"}}, []string{"Bunny CDN"}},
		{"a.cloudfront.net", http.Header{"Cf-Ray": {"test-only"}}, []string{"Cloudflare", "Amazon CloudFront"}},
		{"notcloudfront.net", nil, []string{}},
		{"cloudfront.net.example.com", nil, []string{}},
		{"example.com", http.Header{"Server": {"nginx"}, "X-Cache": {"HIT"}, "Via": {"cdn.example"}}, []string{}},
	} {
		result := identifyCDN(tc.cname, tc.headers, true)
		if !reflect.DeepEqual(result.Providers, tc.providers) {
			t.Fatalf("识别错误：%s %+v", tc.cname, result)
		}
		if (len(tc.providers) > 0) != (result.Status == "detected") {
			t.Fatalf("状态错误：%+v", result)
		}
		if strings.Contains(strings.Join(result.Evidence, " "), "test-only") {
			t.Fatal("不得回传响应头值")
		}
	}
	if identifyCDN("", nil, false).Status != "unknown" {
		t.Fatal("查询失败不能报告未发现 CDN")
	}
}

func diagnosticScanner(t *testing.T, h2 bool, handler http.Handler) (Scanner, *atomic.Int32) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = h2
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	dials := &atomic.Int32{}
	return Scanner{
		Roots: roots, Diagnostics: true,
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		LookupCNAME: func(context.Context, string) (string, error) { return "example.com.", nil },
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:8443" {
				t.Errorf("地址或端口变化：%s", address)
			}
			dials.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}, dials
}

func TestPageStatusUsesOriginalConnectionAndNeverFollowsRedirects(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		for _, status := range []int{200, 302, 403, 404, 503} {
			t.Run(strconv.Itoa(status)+map[bool]string{true: "h2", false: "http1"}[h2], func(t *testing.T) {
				var requests atomic.Int32
				scanner, dials := diagnosticScanner(t, h2, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requests.Add(1)
					if req.Host != "example.com:8443" || req.URL.Path != "/" || req.Method != "GET" {
						t.Errorf("错误请求：%s %s %s", req.Method, req.Host, req.URL.Path)
					}
					w.Header().Set("Cf-Ray", "test-only")
					w.Header().Set("Location", "http://127.0.0.1/private")
					w.WriteHeader(status)
				}))
				results, err := scanner.Scan(context.Background(), []string{"example.com:8443"}, "")
				if err != nil {
					t.Fatal(err)
				}
				r := results[0]
				if r.HTTP.Status != "received" || r.HTTP.StatusCode != status || dials.Load() != 1 || requests.Load() != 1 {
					t.Fatalf("必须复用连接、保留首跳状态：%+v %d %d", r.HTTP, dials.Load(), requests.Load())
				}
				if r.Feasible != h2 || r.CDN.Status != "detected" || r.CDN.Providers[0] != "Cloudflare" {
					t.Fatalf("页面状态不改变握手可用性：%+v", r)
				}
			})
		}
	}
}

func TestDiagnosticsFailuresAndLegacyTasks(t *testing.T) {
	var requests atomic.Int32
	scanner, _ := diagnosticScanner(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(200)
	}))
	scanner.Diagnostics = false
	results, err := scanner.Scan(context.Background(), []string{"example.com:8443"}, "")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(results[0])
	if strings.Contains(string(body), `"cdn"`) || strings.Contains(string(body), `"http"`) || requests.Load() != 0 {
		t.Fatal("旧任务必须保持原报告格式且不访问页面")
	}
	scanner.Diagnostics = true
	scanner.LookupCNAME = func(context.Context, string) (string, error) { return "", errors.New("test DNS error") }
	results, err = scanner.Scan(context.Background(), []string{"example.com:8443"}, "")
	if err != nil || results[0].CDN.Status != "unknown" || results[0].HTTP.Status != "received" {
		t.Fatalf("CDN 查询失败应与页面状态分开：%+v %v", results, err)
	}
	scanner.Roots = x509.NewCertPool()
	before := requests.Load()
	results, err = scanner.Scan(context.Background(), []string{"example.com:8443"}, "")
	if err != nil || requests.Load() != before || results[0].HTTP.Status != "not_checked" || results[0].Feasible {
		t.Fatalf("无效证书不发 HTTP 请求：%+v %v", results, err)
	}
}

func TestSlowPageDoesNotChangeTLSResultAndHasBoundedTimeout(t *testing.T) {
	scanner, _ := diagnosticScanner(t, true, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	}))
	started := time.Now()
	results, err := scanner.Scan(context.Background(), []string{"example.com:8443"}, "")
	if err != nil || !results[0].Feasible || results[0].HTTP.Status != "timeout" || results[0].CDN.Status != "unknown" {
		t.Fatalf("页面超时不能覆盖 TLS 结果：%+v %v", results, err)
	}
	if time.Since(started) > 5*time.Second || results[0].LatencyMS > 2000 {
		t.Fatal("握手延迟不可包含页面等待，页面等待应限时")
	}
}

func TestTaskNegotiatesDiagnosticsAndCanBeCanceled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		called := make(chan bool, 1)
		runner := &Runner{scan: func(ctx context.Context, targets []string, sni string, diagnostics bool) ([]Result, error) {
			called <- diagnostics
			return nil, nil
		}}
		runner.Accept(context.Background(), &Task{ID: "test-diagnostics", ExpiresAt: time.Now().Add(time.Minute).Unix(), Diagnostics: enabled})
		select {
		case actual := <-called:
			if actual != enabled {
				t.Fatal("任务未传递增强检测开关")
			}
		case <-time.After(time.Second):
			t.Fatal("任务未执行")
		}
		runner.Accept(context.Background(), nil)
	}
}
