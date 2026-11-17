package healthcheck

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestRejectNonPublicAndCredentialTargets(t *testing.T) {
	for _, raw := range []string{"http://example.com/", "https://user:secret@example.com/", "https://127.0.0.1/", "https://[::1]/", "https://169.254.169.254/", "https://100.100.100.200/", "https://192.168.1.1/"} {
		if _, _, err := Target(context.Background(), raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestTargetReturnsAlternatingValidatedAddresses(t *testing.T) {
	for _, tc := range []struct {
		name string
		ips  []string
		want []string
	}{
		{
			name: "IPv6 first",
			ips:  []string{"2001:4860::1", "2001:4860::2", "8.8.8.8", "8.8.4.4"},
			want: []string{"[2001:4860::1]:8443", "8.8.8.8:8443", "[2001:4860::2]:8443", "8.8.4.4:8443"},
		},
		{
			name: "IPv4 first",
			ips:  []string{"8.8.8.8", "8.8.4.4", "2001:4860::1"},
			want: []string{"8.8.8.8:8443", "[2001:4860::1]:8443", "8.8.4.4:8443"},
		},
		{
			name: "deduplicate equivalent addresses",
			ips:  []string{"8.8.8.8", "::ffff:8.8.8.8", "8.8.4.4"},
			want: []string{"8.8.8.8:8443", "8.8.4.4:8443"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ips := make([]net.IPAddr, 0, len(tc.ips))
			for _, ip := range tc.ips {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(ip)})
			}
			lookup := func(context.Context, string) ([]net.IPAddr, error) { return ips, nil }
			u, addresses, err := resolveTarget(t.Context(), "https://example.com:8443/check", lookup)
			if err != nil {
				t.Fatal(err)
			}
			if u.Hostname() != "example.com" || !slices.Equal(addresses, tc.want) {
				t.Fatalf("target=%v addresses=%v, want %v", u, addresses, tc.want)
			}
		})
	}
}

func TestTargetRejectsAnyNonPublicAnswer(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "100.100.100.200", "192.168.1.1"} {
		t.Run(ip, func(t *testing.T) {
			lookup := func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(ip)}}, nil
			}
			_, addresses, err := resolveTarget(t.Context(), "https://example.com/", lookup)
			if err == nil || len(addresses) != 0 {
				t.Fatalf("accepted mixed public/private answers: %v, %v", addresses, err)
			}
		})
	}
}

func TestRequestFallsBackToReachableAddress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		tlsFails  bool
	}{
		{name: "IPv6 unavailable", addresses: []string{"[2001:4860::1]:443", "8.8.8.8:443"}},
		{name: "IPv4 unavailable", addresses: []string{"8.8.8.8:443", "[2001:4860::1]:443"}},
		{name: "TLS fails after proxy dial", addresses: []string{"[2001:4860::1]:443", "8.8.8.8:443"}, tlsFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, target, transport := probeServer(t, http.StatusNoContent)
			attempted := []string{}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				attempted = append(attempted, address)
				if address == tc.addresses[0] {
					if tc.tlsFails {
						client, peer := net.Pipe()
						peer.Close()
						return client, nil
					}
					return nil, errors.New("network is unreachable")
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			_, err := request(t.Context(), target, tc.addresses, transport)
			if err != nil || !slices.Equal(attempted, tc.addresses) {
				t.Fatalf("attempted=%v error=%v", attempted, err)
			}
		})
	}
}

func TestRequestAddressesShareEightSecondBudget(t *testing.T) {
	for _, addresses := range [][]string{
		{"8.8.8.8:443"},
		{"[2001:4860::1]:443", "8.8.8.8:443", "[2001:4860::2]:443", "8.8.4.4:443"},
	} {
		name := "multiple addresses"
		if len(addresses) == 1 {
			name = "single address"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, err := url.Parse("https://example.com/check")
				if err != nil {
					t.Fatal(err)
				}
				attempted := []string{}
				dial := func(ctx context.Context, _ string, address string) (net.Conn, error) {
					attempted = append(attempted, address)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				started := time.Now()
				_, err = Request(t.Context(), u, addresses, dial)
				synctest.Wait()
				if err == nil || time.Since(started) != 8*time.Second || !slices.Equal(attempted, addresses) {
					t.Fatalf("shared budget changed: elapsed=%v attempts=%v error=%v", time.Since(started), attempted, err)
				}
			})
		})
	}
}

func TestRequestCancellationStopsLaterAttempts(t *testing.T) {
	u, err := url.Parse("https://example.com/check")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempted := []string{}
	addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
	dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		cancel()
		return nil, context.Canceled
	}
	if _, err := Request(ctx, u, addresses, dial); err == nil || !slices.Equal(attempted, addresses[:1]) {
		t.Fatalf("cancelled probe continued retries: %v %v", attempted, err)
	}
}

func TestRequestReservesTimeForFallback(t *testing.T) {
	server, target, transport := probeServer(t, http.StatusNoContent)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	addresses := []string{"[2001:4860::1]:443", "8.8.8.8:443"}
	attempted := []string{}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		if address == addresses[0] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	_, err := request(ctx, target, addresses, transport)
	if err != nil || ctx.Err() != nil || !slices.Equal(attempted, addresses) {
		t.Fatalf("fallback did not complete before the shared deadline: %v %v", attempted, err)
	}
}

func TestRequestTriesAllFailedAddresses(t *testing.T) {
	u, err := url.Parse("https://example.com/check")
	if err != nil {
		t.Fatal(err)
	}
	addresses := []string{"[2001:4860::1]:443", "8.8.8.8:443", "8.8.4.4:443"}
	attempted := []string{}
	dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		return nil, errors.New("private diagnostic detail")
	}
	_, err = Request(t.Context(), u, addresses, dial)
	if err == nil || strings.Contains(err.Error(), "private diagnostic detail") || !slices.Equal(attempted, addresses) {
		t.Fatalf("attempted=%v error=%v", attempted, err)
	}
}

func TestRequestStopsAfterCancellation(t *testing.T) {
	u, err := url.Parse("https://example.com/check")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dial := func(context.Context, string, string) (net.Conn, error) {
		t.Error("cancelled probe started a connection")
		return nil, errors.New("unexpected connection")
	}
	if _, err := Request(ctx, u, []string{"8.8.8.8:443", "[2001:4860::1]:443"}, dial); err == nil {
		t.Fatal("cancelled probe was marked healthy")
	}
}

func TestRequestKeepsCertificateVerification(t *testing.T) {
	server, target, _ := probeServer(t, http.StatusNoContent)
	attempted := []string{}
	addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	if _, err := Request(t.Context(), target, addresses, dial); err == nil {
		t.Fatal("untrusted certificate was accepted")
	}
	if !slices.Equal(attempted, addresses) {
		t.Fatalf("certificate failure skipped other addresses: %v", attempted)
	}
}

func TestRequestKeepsCertificateHostnameVerification(t *testing.T) {
	server, target, transport := probeServer(t, http.StatusNoContent)
	target.Host = "mismatch.invalid"
	addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
	attempted := []string{}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		attempted = append(attempted, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	_, err := request(t.Context(), target, addresses, transport)
	if err == nil || !slices.Equal(attempted, addresses) {
		t.Fatalf("hostname verification changed: %v %v", attempted, err)
	}
}

func TestRequestKeepsHTTPFailuresAndRejectsRedirects(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, target, transport := probeServer(t, status)
			attempted := []string{}
			addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				attempted = append(attempted, address)
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			_, err := request(t.Context(), target, addresses, transport)
			if err == nil || !strings.Contains(err.Error(), "HTTP") || len(attempted) != 1 {
				t.Fatalf("HTTP failure changed or redirect followed: %v %v", attempted, err)
			}
		})
	}
}

func probeServer(t *testing.T, status int) (*httptest.Server, *url.URL, *http.Transport) {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" || r.URL.Path != "/check" || r.Header.Get("User-Agent") != "YZ-Agent-Health/1" {
			t.Errorf("probe changed HTTPS hostname, path or user agent: %v", r)
		}
		w.Header().Set("Location", "https://127.0.0.1/should-not-follow")
		w.WriteHeader(status)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	if err := server.Certificate().VerifyHostname("example.com"); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = pool
	u, err := url.Parse("https://example.com/check")
	if err != nil {
		t.Fatal(err)
	}
	return server, u, transport
}

func TestListenerTransportMatchesHTTP3AndUDPProtocols(t *testing.T) {
	for _, tc := range []struct {
		protocol, network string
		tls               int
		settings          map[string]any
		tcp               bool
	}{
		{"vless", "xhttp", 1, map[string]any{"alpn": []string{"h3"}}, false},
		{"vless", "xhttp", 1, map[string]any{"alpn": []string{"h2"}}, true},
		{"vless", "tcp", 2, nil, true},
		{"vmess", "kcp", 0, nil, false},
		{"hysteria2", "", 1, nil, false},
		{"tuic", "", 1, nil, false},
		{"wireguard", "", 0, nil, false},
	} {
		if IsTCP(tc.protocol, tc.network, tc.tls, tc.settings) != tc.tcp {
			t.Fatalf("错误的监听探测类型: %+v", tc)
		}
	}
}
