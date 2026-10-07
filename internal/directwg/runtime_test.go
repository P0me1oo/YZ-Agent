package directwg_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	"github.com/sagernet/sing-box/common/dialer"
	wg "github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing/common/control"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/time/rate"
)

func keys(t *testing.T) (string, string) {
	t.Helper()
	k, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

type localDialer struct{}

var _ dialer.UDPListener = localDialer{}

// 与普通直连客户端保持一致，使用标准 UDP Bind。
func (localDialer) UDPListenerControl() (control.Func, bool) {
	return nil, false
}

func (localDialer) DialContext(ctx context.Context, network string, d M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, d.String())
}
func (localDialer) ListenPacket(ctx context.Context, d M.Socksaddr) (net.PacketConn, error) {
	return (&net.ListenConfig{}).ListenPacket(ctx, "udp", "127.0.0.1:0")
}

func wgClient(t *testing.T, priv, pub string, port int, addresses []string) *wg.Endpoint {
	t.Helper()
	a := make([]netip.Prefix, 0, len(addresses))
	for _, v := range addresses {
		a = append(a, netip.MustParsePrefix(v))
	}
	client, e := wg.NewEndpoint(wg.EndpointOptions{Context: context.Background(), Logger: logger.NOP(), Dialer: localDialer{}, PrivateKey: priv, Address: a, MTU: 1420,
		Peers: []wg.PeerOptions{{Endpoint: M.ParseSocksaddr(net.JoinHostPort("127.0.0.1", strconv.Itoa(port))), PublicKey: pub, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}}}})
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Start(false); e != nil {
		t.Fatal("启动测试客户端失败")
	}
	t.Cleanup(func() { client.Close() })
	return client
}
func echoTargets(t *testing.T) (string, string) {
	t.Helper()
	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { tcp.Close() })
	go func() {
		for {
			c, e := tcp.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	udp, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { udp.Close() })
	go func() {
		b := make([]byte, 65535)
		for {
			n, a, e := udp.ReadFrom(b)
			if e != nil {
				return
			}
			_, _ = udp.WriteTo(b[:n], a)
		}
	}()
	return tcp.Addr().String(), udp.LocalAddr().String()
}
func exchange(t *testing.T, client *wg.Endpoint, network, target string) {
	t.Helper()
	_, port, _ := net.SplitHostPort(target)
	target = net.JoinHostPort("198.51.100.10", port)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c, e := client.DialContext(ctx, network, M.ParseSocksaddr(target))
	if e != nil {
		t.Fatal("隧道连接失败:", e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(6 * time.Second))
	data := bytes.Repeat([]byte("wireguard-test"), 20)
	if _, e = c.Write(data); e != nil {
		t.Fatal(e)
	}
	received := make([]byte, len(data))
	if _, e = io.ReadFull(c, received); e != nil {
		t.Fatal(network, "回传失败:", e)
	}
	if !bytes.Equal(data, received) {
		t.Fatal("回传内容不一致")
	}
}

func TestDirectWireGuardTCPUDPAndUsers(t *testing.T) {
	for _, name := range []string{"singbox", "xray"} {
		t.Run(name, func(t *testing.T) {
			serverPriv, serverPub := keys(t)
			clientPriv, clientPub := keys(t)
			socket, e := net.ListenPacket("udp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			port := socket.LocalAddr().(*net.UDPAddr).Port
			socket.Close()
			cfg := config.KernelConfig{Type: name, LogLevel: "error", ConfigDir: t.TempDir()}
			if name == "singbox" {
				cfg.CustomRoute = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1"}}
			} else {
				cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"redirect": "127.0.0.1:0", "finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}}}}}}
			}
			var base kernel.Kernel
			if name == "xray" {
				base = xray.New(cfg)
			} else {
				base = singbox.New(cfg)
			}
			k := directwg.Wrap(base)
			t.Cleanup(k.Stop)
			n := &model.NodeSpec{Protocol: "wireguard", KernelType: name, ListenIP: "127.0.0.1", ServerPort: port, WireGuard: &panel.WireGuardConfig{PrivateKey: serverPriv, Address: []string{"10.0.0.1/32"}, MTU: 1420}}
			users := []model.UserSpec{{ID: 1, UUID: "wg-runtime-user", DeviceLimit: 1, WireGuard: &panel.WireGuardPeer{PublicKey: clientPub, Address: []string{"10.0.0.2/32"}}}}
			if e = k.Start(n, users, kernel.TLSCert{}); e != nil {
				t.Fatal(e)
			}
			tcp, udp := echoTargets(t)
			client := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
			exchange(t, client, "tcp", tcp)
			exchange(t, client, "udp", udp)
			traffic, alive, _, e := k.GetUserTraffic(context.Background())
			if e != nil || traffic[1][0] == 0 || traffic[1][1] == 0 {
				t.Fatal("用户流量未统计")
			}
			payloadBytes := int64(len("wireguard-test") * 20 * 2)
			if traffic[1] != [2]int64{payloadBytes, payloadBytes} {
				t.Fatalf("流量应只计算 TCP/UDP 有效负载：得到 %v，单向应为 %d", traffic[1], payloadBytes)
			}
			if !alive[1]["127.0.0.1"] || alive[1]["10.0.0.2"] {
				t.Fatal("在线来源必须是隧道外地址")
			}
			if _, _, e = k.UpdateUsers(users); e != nil {
				t.Fatal(e)
			}
			exchange(t, client, "tcp", tcp)
			limited := rate.NewLimiter(1040, 1)
			k.SetSpeedLimitFunc(func(string) *rate.Limiter { return limited })
			started := time.Now()
			exchange(t, client, "tcp", tcp)
			if time.Since(started) < 400*time.Millisecond {
				t.Fatal("真实隧道限速未生效")
			}
			k.SetSpeedLimitFunc(nil)
			if _, e = k.RemoveUsers(users); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			_, targetPort, _ := net.SplitHostPort(tcp)
			c, e := client.DialContext(ctx, "tcp", M.ParseSocksaddr(net.JoinHostPort("198.51.100.10", targetPort)))
			if e == nil {
				c.Close()
				t.Fatal("移除用户后仍可建立连接")
			}
			if _, e = k.AddUsers(users); e != nil {
				t.Fatal(e)
			}
			restored := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
			exchange(t, restored, "tcp", tcp)
			for i := 0; i < 2; i++ {
				if e = k.Reload(n, users, kernel.TLSCert{}); e != nil {
					t.Fatal(e)
				}
				reloaded := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
				exchange(t, reloaded, "tcp", tcp)
				exchange(t, reloaded, "udp", udp)
			}
			bad := *n
			bad.WireGuard = nil
			if e = k.Reload(&bad, users, kernel.TLSCert{}); e == nil || !k.IsRunning() {
				t.Fatal("无效配置不应破坏当前运行实例")
			}
			occupied, e := net.ListenPacket("udp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			bad = *n
			bad.ServerPort = occupied.LocalAddr().(*net.UDPAddr).Port
			err := k.Reload(&bad, users, kernel.TLSCert{})
			occupied.Close()
			if err == nil || k.IsRunning() {
				t.Fatal("端口占用没有正确失败")
			}
			if e = k.Reload(n, users, kernel.TLSCert{}); e != nil {
				t.Fatal(e)
			}
			forbidden := *n
			forbidden.SourcePolicy = &sourcepolicy.Policy{BlockCN: true}
			forbidden.SourcePolicyReady = true
			forbidden.SourceBlockCIDRs = []string{"127.0.0.1/32"}
			if e = k.Reload(&forbidden, users, kernel.TLSCert{}); e != nil {
				t.Fatal(e)
			}
			blocked := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
			rejectCtx, rejectCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			denied, denyErr := blocked.DialContext(rejectCtx, "tcp", M.ParseSocksaddr(net.JoinHostPort("198.51.100.10", targetPort)))
			rejectCancel()
			if denyErr == nil {
				denied.Close()
				t.Fatal("来源拦截没有阻止真实 WG 连接")
			}
			if e = k.Reload(n, users, kernel.TLSCert{}); e != nil {
				t.Fatal(e)
			}
			recovered := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
			exchange(t, recovered, "tcp", tcp)
			rotatedPrivate, rotatedPublic := keys(t)
			rotated := users[0]
			rotated.WireGuard = model.CloneWireGuardPeer(rotated.WireGuard)
			rotated.WireGuard.PublicKey = rotatedPublic
			if _, _, e = k.UpdateUsers([]model.UserSpec{rotated}); e != nil {
				t.Fatal(e)
			}
			rotationCtx, rotationCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			oldConn, oldErr := recovered.DialContext(rotationCtx, "tcp", M.ParseSocksaddr(net.JoinHostPort("198.51.100.10", targetPort)))
			rotationCancel()
			if oldErr == nil {
				oldConn.Close()
				t.Fatal("轮换后旧密钥仍可使用")
			}
			newClient := wgClient(t, rotatedPrivate, serverPub, port, rotated.WireGuard.Address)
			exchange(t, newClient, "tcp", tcp)
			users = []model.UserSpec{rotated}
			recovered = newClient
			expired := users[0]
			expired.WireGuard = model.CloneWireGuardPeer(expired.WireGuard)
			expired.WireGuard.ExpiresAt = time.Now().Unix() - 1
			if _, _, e = k.UpdateUsers([]model.UserSpec{expired}); e != nil {
				t.Fatal(e)
			}
			expiredCtx, expiredCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			expiredConn, expiredErr := recovered.DialContext(expiredCtx, "tcp", M.ParseSocksaddr(net.JoinHostPort("198.51.100.10", targetPort)))
			expiredCancel()
			if expiredErr == nil {
				expiredConn.Close()
				t.Fatal("已到期用户仍能转发")
			}
			before, _, _, _ := k.GetUserTraffic(context.Background())
			k.Stop()
			after, _, _, _ := k.GetUserTraffic(context.Background())
			if after[1][0] < before[1][0] || after[1][1] < before[1][1] {
				t.Fatal("停止后累计流量减少")
			}
		})
	}
}
