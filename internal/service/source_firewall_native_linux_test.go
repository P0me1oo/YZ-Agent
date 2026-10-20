//go:build linux

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type nativeSourceSOCKSProbe struct {
	Address string
	Allowed bool
}

func TestNativeSourceSOCKSProbe(t *testing.T) {
	raw := os.Getenv("YZ_SOURCE_SOCKS_PROBES")
	if raw == "" {
		t.Skip("由隔离客户端调用")
	}
	var probes []nativeSourceSOCKSProbe
	if err := json.Unmarshal([]byte(raw), &probes); err != nil {
		t.Fatal(err)
	}
	for _, probe := range probes {
		conn, err := net.DialTimeout("tcp", probe.Address, 750*time.Millisecond)
		if !probe.Allowed {
			if err == nil {
				conn.Close()
				t.Errorf("名单外来源仍完成 TCP 连接：%s", probe.Address)
			}
			continue
		}
		if err != nil {
			t.Errorf("允许来源连接失败：%v", err)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, err = conn.Write([]byte{5, 2, 0, 2})
		var response [2]byte
		if err == nil {
			_, err = io.ReadFull(conn, response[:])
		}
		conn.Close()
		if err != nil || response != [2]byte{5, 2} {
			t.Errorf("允许来源没有收到 SOCKS5 用户认证协商：%v %x", err, response)
		}
	}
}

func TestNativeSourceSOCKSRuntime(t *testing.T) {
	if os.Getenv("YZ_FIREWALL_NATIVE") != "1" {
		t.Skip("需要专用 rootfs 和网络命名空间")
	}
	marker, err := os.ReadFile("/run/yz-firewall-test")
	if err != nil || string(marker) != "isolated-rootfs\n" || os.Geteuid() != 0 {
		t.Fatal("拒绝在未隔离环境中运行")
	}
	if _, err := net.InterfaceByName("yzfw0"); err != nil {
		t.Fatal("缺少隔离接口")
	}
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			if os.Getenv("YZ_FIREWALL_BACKEND") == "ufw" {
				for _, args := range [][]string{
					{"allow", "from", "192.0.2.2", "to", "any", "port", "28463", "comment", "manual-service-whitelist"},
					{"deny", "28463", "comment", "manual-service-deny"},
				} {
					if output, err := exec.Command("ufw", args...).CombinedOutput(); err != nil {
						t.Fatalf("准备隔离手工规则失败: %v %s", err, output)
					}
				}
			}
			cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
			var core kernel.Kernel
			if kind == "xray" {
				core = xray.New(cfg)
			} else {
				core = singbox.New(cfg)
			}
			s := newTestService(&fakeKernel{})
			s.kernel, s.cfg.Kernel = core, cfg
			manager, err := firewall.New(config.FirewallConfig{Backend: os.Getenv("YZ_FIREWALL_BACKEND"), SourceBackend: os.Getenv("YZ_FIREWALL_REDIRECT"), StateDir: t.TempDir()}, filepath.Join(t.TempDir(), "config.yml"))
			if err != nil {
				t.Fatal(err)
			}
			s.SetFirewallController(manager)
			s.timeConsumer = "source-socks/" + kind
			t.Cleanup(func() {
				s.stopKernel()
				if err := manager.Close(); err != nil {
					t.Error(err)
				}
			})
			s.lastConfig = &model.NodeSpec{Protocol: "socks", ServerPort: 28463, ListenIP: "::", SourcePolicy: &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"198.51.100.7"}}}
			s.lastUsers = []model.UserSpec{{ID: 1, UUID: randomRelayTestID()}}
			probe := func(allowed bool, family6 ...bool) {
				t.Helper()
				allowed6 := allowed
				if len(family6) > 0 {
					allowed6 = family6[0]
				}
				probes := []nativeSourceSOCKSProbe{{Address: "192.0.2.1:28463", Allowed: allowed}, {Address: "[2001:db8:1::1]:28463", Allowed: allowed6}}
				data, _ := json.Marshal(probes)
				exe, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("ip", "netns", "exec", "yzfw-client", exe, "-test.run=^TestNativeSourceSOCKSProbe$", "-test.timeout=15s")
				cmd.Env = append(os.Environ(), "YZ_SOURCE_SOCKS_PROBES="+string(data))
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("SOCKS5 握手隔离探测失败：%v\n%s", err, output)
				}
			}
			if !s.startKernel(context.Background(), s.lastConfig, s.lastUsers) {
				if os.Getenv("YZ_FIREWALL_REDIRECT") == "nftables" {
					if output, debugErr := exec.Command("nft", "--json", "--numeric-priority", "list", "ruleset").CombinedOutput(); debugErr == nil {
						t.Logf("nft 规则集：%s", output)
					}
				}
				t.Fatalf("受保护启动失败：%v", s.runtimeError)
			}
			probe(false)
			allowed := *s.lastConfig
			allowed.SourcePolicy = &sourcepolicy.Policy{Mode: "whitelist", AllowIPs: []string{"192.0.2.2", "2001:db8:1::2"}}
			if !s.applyConfigUpdate(context.Background(), &allowed, computeConfigHash(&allowed)) {
				t.Fatalf("允许来源更新失败：%v", s.runtimeError)
			}
			probe(true)
			if !s.applyChanges(context.Background(), true, false) {
				t.Fatal("重复重载失败")
			}
			probe(true)
			blocked := allowed
			blocked.SourcePolicy = &sourcepolicy.Policy{Mode: "blacklist", BlockIPs: []string{"192.0.2.2", "2001:db8:1::2"}}
			if !s.applyConfigUpdate(context.Background(), &blocked, computeConfigHash(&blocked)) {
				t.Fatal("黑名单更新失败")
			}
			probe(false)
			if os.Getenv("YZ_FIREWALL_BACKEND") == "ufw" {
				manual := blocked
				manual.AutoFirewall = new(false)
				if !s.applyConfigUpdate(context.Background(), &manual, computeConfigHash(&manual)) {
					t.Fatalf("退出防火墙接管失败: %v", s.runtimeError)
				}
				probe(true, false)
				allowed.AutoFirewall = new(true)
				if !s.applyConfigUpdate(context.Background(), &allowed, computeConfigHash(&allowed)) {
					t.Fatalf("重新接管失败: %v", s.runtimeError)
				}
				probe(true)
			}
			t.Log(fmt.Sprintf("%s：名单外来源没有 TCP/SOCKS5 响应，允许来源可协商认证，黑白名单变更及重复重载通过", kind))
		})
	}
}
