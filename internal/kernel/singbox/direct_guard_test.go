package singbox

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestBuildRoutesBlocksUnspecifiedAddresses(t *testing.T) {
	rules := buildRoutes(nil, nil, nil)["rules"].([]M)
	want := map[string]bool{"0.0.0.0/8": false, "::/127": false, "127.0.0.0/8": false, "::1/128": false}
	for _, rule := range rules[:2] {
		for _, cidr := range rule["ip_cidr"].([]string) {
			if _, ok := want[cidr]; ok {
				want[cidr] = true
			}
		}
	}
	// ::1 已包含在 ::/127 中，不再单独列出。
	delete(want, "::1/128")
	for cidr, found := range want {
		if !found {
			t.Errorf("default block rules missing %s", cidr)
		}
	}
}

// 默认规则必须先解析域名再拦截；显式放行的自定义规则保持优先。
func TestSingBoxRuntimeDirectGuardBlocksPrivateDomains(t *testing.T) {
	echo := runtimeEcho(t)
	literal := echo
	domain := singM.Socksaddr{Fqdn: "localhost", Port: echo.Port}

	for _, tc := range []struct {
		name    string
		allowed bool
	}{
		{name: "blocked", allowed: false},
		{name: "explicit_direct", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := runtimeNode(t, "socks")
			if !tc.allowed {
				node.CustomRoutes = nil
			}
			user := runtimeUser(t, 311)
			s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Stop)
			client := runtimeClient(t, node, user)
			for _, destination := range []singM.Socksaddr{literal, domain} {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				conn, err := runtimeConnect(ctx, client, destination)
				if err == nil {
					err = runtimeExchangeResult(conn)
					_ = conn.Close()
				}
				cancel()
				if tc.allowed && err != nil {
					t.Fatalf("%s should pass explicit direct rule: %v", destination, err)
				}
				if !tc.allowed && err == nil {
					t.Fatalf("%s reached a loopback service", destination)
				}
			}
		})
	}
}

// 放行与撤销走原有路由热更新；被拦的域名不能先建立 TCP 连接再断开。
func TestPrivateDomainGuardReloadAndNoPrivateDial(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = c.Close()
		}
	}()
	node := runtimeNode(t, "socks")
	allow := node.CustomRoutes
	node.CustomRoutes = nil
	user := runtimeUser(t, 312)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	client := runtimeClient(t, node, user)
	destination := singM.Socksaddr{Fqdn: "localhost", Port: uint16(l.Addr().(*net.TCPAddr).Port)}
	check := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		conn, err := runtimeConnect(ctx, client, destination)
		if err == nil {
			_ = runtimeExchangeResult(conn)
			_ = conn.Close()
		}
	}
	check()
	if accepted.Load() != 0 {
		t.Fatal("拦截之前不应连接内网服务")
	}
	updated := *node
	updated.CustomRoutes = allow
	if err := s.Reload(&updated, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	check()
	if accepted.Load() != 1 {
		t.Fatal("显式放行后应能连接内网服务")
	}
	if err := s.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	check()
	if accepted.Load() != 1 {
		t.Fatal("撤销放行后仍连接了内网服务")
	}
}
