package healthcheck

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestProberReusesSuccessfulAddressAndRecoversAfterChange(t *testing.T) {
	server, target, transport := probeServer(t, http.StatusNoContent)
	addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
	original := slices.Clone(addresses)
	good := addresses[1]
	var mu sync.Mutex
	attempts := []string{}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		attempts = append(attempts, address)
		mu.Unlock()
		if address != good {
			return nil, errors.New("network is unreachable")
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	p := &Prober{}
	check := func(route string, available, want []string) {
		t.Helper()
		attempts = []string{}
		if _, err := p.request(
			t.Context(),
			route,
			target,
			available,
			transport,
		); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(attempts, want) || !slices.Equal(addresses, original) {
			t.Fatalf("出站地址复用错误：attempts=%v want=%v DNS=%v", attempts, want, addresses)
		}
	}
	check("relay-1", addresses, addresses)
	check("relay-1", addresses, addresses[1:])
	// 同一目标的另一路落地必须单独确认，不能沿用其他落地的能力。
	check("relay-2", addresses, addresses)
	good = addresses[0]
	check("relay-1", addresses, []string{addresses[1], addresses[0]})
	check("relay-1", addresses, addresses[:1])
	check("relay-2", addresses[:1], addresses[:1])
}

func TestProberRemembersOnlyCompletedHTTPSRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "TLS failure falls back", status: http.StatusNoContent},
		{name: "HTTP failure stops", status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, target, transport := probeServer(t, tc.status)
			addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
			attempts := []string{}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				attempts = append(attempts, address)
				if address == addresses[0] && tc.status == http.StatusNoContent {
					client, peer := net.Pipe()
					peer.Close()
					return client, nil
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			p := &Prober{}
			for round := range 2 {
				attempts = []string{}
				_, err := p.request(
					t.Context(),
					"relay-1",
					target,
					addresses,
					transport,
				)
				want := addresses[:1]
				if tc.status == http.StatusNoContent {
					want = addresses
					if round == 1 {
						want = addresses[1:]
					}
				}
				if (err == nil) != (tc.status == http.StatusNoContent) || !slices.Equal(attempts, want) {
					t.Fatalf("HTTPS 回退或 HTTP 拒绝语义变化：attempts=%v want=%v error=%v", attempts, want, err)
				}
			}
		})
	}
}

func TestProberConcurrentRoutesKeepSeparatePreferences(t *testing.T) {
	server, target, base := probeServer(t, http.StatusNoContent)
	p := &Prober{}
	addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
	var group sync.WaitGroup
	for i := range 4 {
		group.Go(func() {
			transport := base.Clone()
			good := addresses[i%2]
			calls := 0
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				calls++
				if address != good {
					return nil, errors.New("unavailable address family")
				}
				return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
			}
			for round := range 5 {
				calls = 0
				if _, err := p.request(
					t.Context(),
					strconv.Itoa(i),
					target,
					addresses,
					transport,
				); err != nil {
					t.Error(err)
				}
				if round > 0 && calls != 1 {
					t.Errorf("出站 %d 仍重复尝试不可用地址：%d", i, calls)
				}
			}
		})
	}
	group.Wait()
}

func TestProberPreservesBudgetAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		target, err := url.Parse("https://example.com/check")
		if err != nil {
			t.Fatal(err)
		}
		addresses := []string{"8.8.8.8:443", "[2001:4860::1]:443"}
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		started := time.Now()
		p := &Prober{}
		if _, err := p.Request(
			t.Context(),
			"relay-1",
			target,
			addresses,
			dial,
		); err == nil {
			t.Fatal("全部地址失败仍判为健康")
		}
		synctest.Wait()
		if time.Since(started) != 8*time.Second {
			t.Fatalf("多地址检查扩大了总预算：%v", time.Since(started))
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		called := false
		dial = func(context.Context, string, string) (net.Conn, error) {
			called = true
			return nil, errors.New("unexpected dial")
		}
		if _, err := p.Request(
			ctx,
			"relay-1",
			target,
			addresses,
			dial,
		); !errors.Is(err, context.Canceled) || called {
			t.Fatalf("取消后继续检查：called=%v error=%v", called, err)
		}
	})
}
