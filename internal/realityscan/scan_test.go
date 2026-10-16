package realityscan

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTargetsAndBounds(t *testing.T) {
	for _, value := range Defaults {
		if strings.Contains(value, "cloudflare") {
			t.Fatal("默认列表不能包含 Cloudflare")
		}
	}
	items, err := expand([]string{"8.8.8.0/24", "example.com:8443", "example.com:8443"}, "")
	if err != nil || len(items) != 257 || items[256].sni != "example.com" || items[256].port != 8443 {
		t.Fatalf("展开结果错误：%d %v", len(items), err)
	}
	for _, values := range [][]string{
		{"8.8.0.0/16"}, {"https://example.com"}, {"example.com:0"}, {"example.com:65536"},
		{"8.8.8.0/24", "9.9.9.0/24", "1.1.1.1"}, {"[fe80::1%eth0]:443"}, {"example.com/path"},
	} {
		if _, err := expand(values, ""); err == nil {
			t.Errorf("应拒绝 %v", values)
		}
	}
	if _, err := expand([]string{"example.com", "example.net"}, "example.com"); err == nil {
		t.Fatal("SNI 只用于单个目标")
	}
	host, port, err := ParseTarget("[2606:4700::1111]:8443")
	if err != nil || host != "2606:4700::1111" || port != 8443 {
		t.Fatalf("IPv6：%s %d %v", host, port, err)
	}
}

func TestPrivateAddressesAndDNSRebindingAreBlocked(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "::ffff:127.0.0.1", "64:ff9b::a00:1", "2001:db8::1", "2002:0a00:0001::1", "fc00::1", "fe80::1"} {
		if publicAddress(netip.MustParseAddr(value)) {
			t.Errorf("应拒绝 %s", value)
		}
	}
	for _, value := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(value)) {
			t.Errorf("应接受公网 %s", value)
		}
	}
	dialed := false
	scanner := Scanner{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
		},
		Dial: func(context.Context, string, string) (net.Conn, error) { dialed = true; return nil, nil },
	}
	results, err := scanner.Scan(context.Background(), []string{"example.com"}, "")
	if err != nil || dialed || results[0].Reason != "private_target" {
		t.Fatalf("混合 DNS 必须在连接前拦截：%+v %v", results, err)
	}
}

func localScanner(t *testing.T, h2, tls13 bool) Scanner {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.EnableHTTP2 = h2
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, CurvePreferences: []tls.CurveID{tls.X25519}}
	if !tls13 {
		server.TLS.MaxVersion = tls.VersionTLS12
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return Scanner{
		Roots: roots,
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !strings.HasPrefix(address, "8.8.8.8:") {
				t.Errorf("只能连接已核对的地址：%s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}
}

func TestTLSProbeAndCertificateDiscovery(t *testing.T) {
	scanner := localScanner(t, true, true)
	results, err := scanner.Scan(context.Background(), []string{"example.com:443"}, "")
	if err != nil || len(results) != 1 {
		t.Fatalf("%+v %v", results, err)
	}
	r := results[0]
	if !r.Feasible || !r.CertValid || !r.H2 || !r.TLS13 || !r.X25519 || r.LatencyMS <= 0 || r.CertChainBytes == 0 {
		t.Fatalf("正常握手未通过：%+v", r)
	}
	results, err = scanner.Scan(context.Background(), []string{"8.8.8.8"}, "")
	if err != nil || !results[0].Feasible || results[0].Host != "example.com" || results[0].DiscoveredIP != "8.8.8.8" {
		t.Fatalf("证书发现后必须复测：%+v %v", results, err)
	}
	results, err = scanner.Scan(context.Background(), []string{"example.net"}, "")
	if err != nil || results[0].Feasible || results[0].Reason != "certificate_invalid" || !results[0].CertChainValid {
		t.Fatalf("名称不匹配：%+v %v", results, err)
	}
	scanner.Roots = x509.NewCertPool()
	results, _ = scanner.Scan(context.Background(), []string{"example.com"}, "")
	if results[0].CertValid || results[0].Feasible {
		t.Fatal("不受信任的证书不能回填")
	}
}

func TestTLSRequirementsAndCancellation(t *testing.T) {
	for _, setup := range []struct {
		h2, tls13 bool
		reason    string
	}{{true, false, "tls13_required"}, {false, true, "h2_required"}} {
		scanner := localScanner(t, setup.h2, setup.tls13)
		results, err := scanner.Scan(context.Background(), []string{"example.com"}, "")
		if err != nil || results[0].Feasible || results[0].Reason != setup.reason {
			t.Fatalf("%+v %v", results, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanner := localScanner(t, true, true)
	if _, err := scanner.Scan(ctx, []string{"example.com"}, ""); err == nil {
		t.Fatal("取消后不能报告成功")
	}
}

func TestRunnerRetriesReuseResultsAndCancellation(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 3)
	finish := make(chan struct{})
	runner := &Runner{scan: func(ctx context.Context, values []string, sni string) ([]Result, error) {
		calls.Add(1)
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-finish:
			return []Result{{Host: "example.com"}}, nil
		}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	task := &Task{ID: "test-task-1", Targets: []string{"example.com"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	runner.Accept(ctx, task)
	<-started
	runner.Accept(ctx, task)
	if calls.Load() != 1 || runner.Snapshot().Status != "running" {
		t.Fatal("重复任务不得再次执行")
	}
	close(finish)
	deadline := time.After(time.Second)
	for runner.Snapshot().Status == "running" {
		select {
		case <-deadline:
			t.Fatal("结果没有完成")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	before := runner.Snapshot()
	runner.Accept(ctx, task)
	if calls.Load() != 1 || !reflect.DeepEqual(before, runner.Snapshot()) {
		t.Fatal("确认丢失时应重发原结果")
	}
	runner.Accept(ctx, nil)
	if runner.Snapshot() != nil {
		t.Fatal("确认后不再反复上传结果")
	}
	runner.Accept(ctx, task)
	if calls.Load() != 1 {
		t.Fatal("已完成任务不可重跑")
	}
}

func TestRunnerCancelsSupersededWorkAndIgnoresLateCompletion(t *testing.T) {
	started := make(chan string, 2)
	canceled := make(chan struct{}, 2)
	runner := &Runner{scan: func(ctx context.Context, values []string, sni string) ([]Result, error) {
		started <- values[0]
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := &Task{ID: "first", Targets: []string{"first.example"}, ExpiresAt: time.Now().Add(time.Minute).Unix()}
	second := &Task{ID: "second", Targets: []string{"second.example"}, ExpiresAt: first.ExpiresAt}
	runner.Accept(ctx, first)
	<-started
	runner.Accept(ctx, second)
	<-canceled
	<-started
	if report := runner.Snapshot(); report.ID != "second" || report.Status != "running" {
		t.Fatalf("旧结果覆盖新任务：%+v", report)
	}
	runner.Accept(ctx, nil)
	<-canceled
	if runner.Snapshot() != nil {
		t.Fatal("任务撤销后不保留在途结果")
	}
}
