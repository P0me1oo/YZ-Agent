//go:build with_mihomo_integration

package directwg_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"golang.org/x/net/proxy"
)

// 独立启动 Mihomo，不读取桌面客户端配置，也不接管系统代理。
func TestDirectWireGuardMihomo(t *testing.T) {
	binary := os.Getenv("WG_MIHOMO_BIN")
	if binary == "" {
		t.Fatal("WG_MIHOMO_BIN 未设置")
	}
	for _, name := range []string{"singbox", "xray"} {
		t.Run(name, func(t *testing.T) {
			serverPrivate, serverPublic := keys(t)
			clientPrivate, clientPublic := keys(t)
			listen, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listen.LocalAddr().(*net.UDPAddr).Port
			listen.Close()
			cfg := config.KernelConfig{Type: name, LogLevel: "error", ConfigDir: t.TempDir()}
			if name == "singbox" {
				cfg.CustomRoute = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1"}}
			} else {
				cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"redirect": "127.0.0.1:0", "finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}}}}}}
			}
			var base kernel.Kernel
			if name == "singbox" {
				base = singbox.New(cfg)
			} else {
				base = xray.New(cfg)
			}
			k := directwg.Wrap(base)
			t.Cleanup(k.Stop)
			n := &model.NodeSpec{Protocol: "wireguard", KernelType: name, ListenIP: "127.0.0.1", ServerPort: port, WireGuard: &panel.WireGuardConfig{PrivateKey: serverPrivate, Address: []string{"10.0.0.1/32", "fd7a:797a:1::1/128"}, MTU: 1420}}
			users := []model.UserSpec{{ID: 1, UUID: "wg-mihomo-test-user", DeviceLimit: 1, WireGuard: &panel.WireGuardPeer{PublicKey: clientPublic, Address: []string{"10.0.0.2/32", "fd7a:797a:1::2/128"}}}}
			if err = k.Start(n, users, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			tcp, _ := echoTargets(t)
			reserve, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			proxyAddr := reserve.Addr().String()
			proxyPort := reserve.Addr().(*net.TCPAddr).Port
			reserve.Close()
			settings := map[string]any{
				"mixed-port": proxyPort, "bind-address": "127.0.0.1", "allow-lan": false, "mode": "rule", "log-level": "debug",
				"proxies": []map[string]any{{"name": "ordinary-wg", "type": "wireguard", "server": "127.0.0.1", "port": port, "ip": "10.0.0.2", "ipv6": "fd7a:797a:1::2", "private-key": clientPrivate, "public-key": serverPublic, "udp": true, "mtu": 1420, "persistent-keepalive": 25}},
				"rules":   []string{"MATCH,ordinary-wg"},
			}
			dir := t.TempDir()
			data, err := json.Marshal(settings)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "client.json")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-d", dir, "-f", path)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				if t.Failed() {
					message := strings.NewReplacer(serverPrivate, "[密钥]", serverPublic, "[公钥]", clientPrivate, "[密钥]", clientPublic, "[公钥]").Replace(output.String())
					if len(message) > 4000 {
						message = message[len(message)-4000:]
					}
					t.Log(message)
				}
			})
			ready := false
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				c, e := net.DialTimeout("tcp", proxyAddr, 200*time.Millisecond)
				if e == nil {
					c.Close()
					ready = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !ready {
				t.Fatal("独立 Mihomo 未就绪")
			}
			dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, &net.Dialer{Timeout: 6 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, targetPort, _ := net.SplitHostPort(tcp)
			probe := func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				conn, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", net.JoinHostPort("198.51.100.10", targetPort))
				if err != nil {
					return err
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				payload := bytes.Repeat([]byte("mihomo-wg"), 32)
				if _, err = conn.Write(payload); err != nil {
					return err
				}
				got := make([]byte, len(payload))
				if _, err = io.ReadFull(conn, got); err != nil {
					return err
				}
				if !bytes.Equal(got, payload) {
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			// Mihomo 先开放本地监听，完成配置后才切到 Running；端口存在不代表转发就绪。
			deadline = time.Now().Add(20 * time.Second)
			for {
				err = probe()
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Mihomo WG 未就绪", err)
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err = probe(); err != nil {
				t.Fatal("Mihomo WG 就绪后的独立连接失败", err)
			}
		})
	}
}
