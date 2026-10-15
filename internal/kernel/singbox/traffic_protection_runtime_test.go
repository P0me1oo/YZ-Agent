//go:build with_quic

package singbox

import (
	"context"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestTrafficProtectionImmediateStopRuntime(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, protocol := range []string{"vless", "shadowsocks", "trojan"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				node, user, cert := runtimeNode(t, protocol), runtimeUser(t, 990), runtimeCertificate(t)
				cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
				var core kernel.Kernel
				if kind == "xray" {
					// 共用客户端夹具，但 Xray 使用自己的路由格式和本地测试出口白名单。
					node.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"127.0.0.1/32"}, "outboundTag": "direct"}}
					cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{
						"finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}}},
					}}}
					core = xray.New(cfg)
				} else {
					core = New(cfg)
				}
				core = directwg.Wrap(core)
				if err := core.Start(node, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				echo := runtimeEcho(t)
				var tlsOptions map[string]any
				if kind == "xray" && protocol == "trojan" {
					tlsOptions = map[string]any{"enabled": true, "insecure": true, "server_name": "localhost", "alpn": []string{"http/1.1"}}
				}
				client := runtimeClientWithTLS(t, node, user, tlsOptions)
				conn := runtimeDial(t, client, echo)
				runtimeExchange(t, conn)
				before, _, _, _ := core.GetUserTraffic(context.Background())
				start := time.Now()
				core.(kernel.ImmediateStopper).StopNow()
				if time.Since(start) >= 3*time.Second {
					t.Fatal("保护仍等待自然排空")
				}
				if core.IsRunning() {
					t.Fatal("保护后核心仍在运行")
				}
				if err := runtimeExchangeResult(conn); err == nil {
					t.Fatal("保护后已有连接仍可传输")
				}
				runtimeReject(t, client, echo)
				core.(kernel.ImmediateStopper).StopNow()
				after, _, _, _ := core.GetUserTraffic(context.Background())
				if after[user.ID][0] < before[user.ID][0] || after[user.ID][1] < before[user.ID][1] {
					t.Fatal("立即停止丢失代理历史统计")
				}
				if err := core.Start(node, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
				runtimeExchange(t, runtimeDial(t, client, echo))
			})
		}
	}
}

func TestTrafficProtectionRelayNoDirectFallbackRuntime(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			user := runtimeUser(t, 991)
			landing := runtimeNode(t, "vless")
			landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: relayCredential(runtimeUser(t, 992), 0)}}
			landingCore := relayStartCore(t, kind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"))
			entry := runtimeNode(t, "vless")
			entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{NodeID: 12, RouteID: 12, Tag: "relay-12", Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort, VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}}}}
			entryCore := relayStartCore(t, kind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
			routed := user
			routed.UUID = relayCredential(user, 12)
			client := runtimeClient(t, entry, routed)
			for _, network := range []string{"tcp", "udp"} {
				relayMarkerExchange(t, client, "landing", network)
			}
			landingCore.(kernel.ImmediateStopper).StopNow()
			for _, network := range []string{"tcp", "udp"} {
				relayMarkerReject(t, client, network)
			}
			protected := *entry
			relay := *entry.Relay
			protected.Relay = &relay
			relay.Children = nil
			relay.BlockedRouteIDs = []int{12}
			for range 2 {
				if err := entryCore.Reload(&protected, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerReject(t, runtimeClient(t, &protected, routed), network)
				}
			}
			direct := user
			direct.UUID = relayCredential(user, 11)
			relayMarkerExchange(t, runtimeClient(t, &protected, direct), "direct", "tcp")
		})
	}
}

func TestTrafficProtectionStopsRetiredXrayRuntime(t *testing.T) {
	node, user := runtimeNode(t, "vless"), runtimeUser(t, 993)
	node.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"127.0.0.1/32"}, "outboundTag": "direct"}}
	core := xray.New(config.KernelConfig{Type: "xray", LogLevel: "fatal", ConfigDir: t.TempDir(), CustomOutbound: []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{"finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}}}}}}})
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	echo := runtimeEcho(t)
	old := runtimeDial(t, runtimeClient(t, node, user), echo)
	runtimeExchange(t, old)
	next := *node
	next.ServerPort = runtimeDualPort(t)
	if err := core.Reload(&next, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	// 普通热重载保留旧会话；额度保护必须连同旧实例一起关闭。
	runtimeExchange(t, old)
	current := runtimeDial(t, runtimeClient(t, &next, user), echo)
	runtimeExchange(t, current)
	core.StopNow()
	if runtimeExchangeResult(old) == nil || runtimeExchangeResult(current) == nil {
		t.Fatal("保护遗漏了重载前后的连接")
	}
	runtimeReject(t, runtimeClient(t, &next, user), echo)
}
