//go:build with_quic

package singbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

// 使用正式固定依赖启动两种核心，无前置 IP 名单时接收 v1/v2，普通连接仍可认证。
func TestNodeProxyProtocolSwitchRuntime(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"shadowsocks", "shadowsocks2022-128", "shadowsocks2022-256", "vless", "trojan"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				node := runtimeNode(t, protocol)
				node.AcceptProxyProtocol = new(true)
				user := runtimeUser(t, 2501)
				target := runtimeEcho(t)
				echo := singM.ParseSocksaddr("198.51.100.10:80")
				cert := kernel.TLSCert{}
				if protocol == "trojan" {
					cert = runtimeCertificate(t)
				}
				core := relayStartCore(t, kind, node, []model.UserSpec{user}, cert, target.String())
				for _, version := range []byte{1, 2} {
					forwarded := *node
					forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, version, "203.0.114.96")
					conn := runtimeDial(t, proxySwitchClient(t, &forwarded, user), echo)
					runtimeExchange(t, conn)
					_, ips, _, err := core.GetUserTraffic(context.Background())
					if err != nil || !ips[user.ID]["203.0.114.96"] {
						t.Fatal("PROXY 真实来源未进入统计")
					}
					conn.Close()
				}
				runtimeExchange(t, runtimeDial(t, proxySwitchClient(t, node, user), echo))
				deadline := time.Now().Add(3 * time.Second)
				want := [2]int64{3 * int64(len(runtimePayload)), 3 * int64(len(runtimePayload))}
				for {
					traffic, _, _, err := core.GetUserTraffic(context.Background())
					if err == nil && traffic[user.ID] == want {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("业务流量应排除 PROXY 头: got=%v want=%v err=%v", traffic[user.ID], want, err)
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
		}
	}
}

func TestNodeProxyProtocolSwitchLifecycleAndIsolation(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			user := runtimeUser(t, 2502)
			target := runtimeEcho(t)
			echo := singM.ParseSocksaddr("198.51.100.10:80")
			node := runtimeNode(t, "vless")
			node.AcceptProxyProtocol = new(true)
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
			setter := core.(interface {
				SetDeviceIPExclude([]string) ([]string, bool)
			})
			other := runtimeNode(t, "vless")
			other.AcceptProxyProtocol = new(false)
			otherCore := relayStartCore(t, kind, other, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
			otherCore.(interface {
				SetDeviceIPExclude([]string) ([]string, bool)
			}).SetDeviceIPExclude([]string{"127.0.0.1"})
			otherForwarded := *other
			otherForwarded.ServerPort = proxyProtocolForwarder(t, other.ServerPort, 2, "203.0.114.97")
			runtimeReject(t, runtimeClient(t, &otherForwarded, user), echo)
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, other, user), echo))

			for i, enabled := range []bool{true, false, true} {
				changed := *node
				changed.AcceptProxyProtocol = new(enabled)
				changed.NetworkSettings = map[string]any{"acceptProxyProtocol": true}
				for range 2 {
					if err := core.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
				}
				setter.SetDeviceIPExclude([]string{"127.0.0.1"})
				for _, version := range []byte{1, 2} {
					t.Run(fmt.Sprintf("step%d/v%d", i, version), func(t *testing.T) {
						forwarded := changed
						forwarded.ServerPort = proxyProtocolForwarder(t, changed.ServerPort, version, "203.0.114.97")
						client := runtimeClient(t, &forwarded, user)
						if enabled {
							runtimeExchange(t, runtimeDial(t, client, echo))
						} else {
							runtimeReject(t, client, echo)
						}
					})
				}
				setter.SetDeviceIPExclude(nil)
				withoutList := changed
				withoutList.ServerPort = proxyProtocolForwarder(t, changed.ServerPort, 2, "203.0.114.97")
				if enabled {
					runtimeExchange(t, runtimeDial(t, runtimeClient(t, &withoutList, user), echo))
				} else {
					runtimeReject(t, runtimeClient(t, &withoutList, user), echo)
				}
				runtimeExchange(t, runtimeDial(t, runtimeClient(t, &changed, user), echo))
				runtimeReject(t, runtimeClient(t, &otherForwarded, user), echo)
			}
			core.Stop()
			if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			forwarded := *node
			forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.97")
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, &forwarded, user), echo))
		})
	}
}

func TestNodeProxyProtocolLegacyTrust(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			node := runtimeNode(t, "vless")
			node.NetworkSettings = map[string]any{"acceptProxyProtocol": true}
			user := runtimeUser(t, 2503)
			target := runtimeEcho(t)
			echo := singM.ParseSocksaddr("198.51.100.10:80")
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
			setter := core.(interface {
				SetDeviceIPExclude([]string) ([]string, bool)
			})
			forwarded := *node
			forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.98")
			client := runtimeClient(t, &forwarded, user)
			runtimeReject(t, client, echo)
			setter.SetDeviceIPExclude([]string{"127.0.0.1"})
			runtimeExchange(t, runtimeDial(t, client, echo))
			setter.SetDeviceIPExclude(nil)
			runtimeReject(t, client, echo)
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
		})
	}
}

func proxySwitchClient(t *testing.T, node *model.NodeSpec, user model.UserSpec) adapter.Outbound {
	if node.Protocol == "trojan" {
		return runtimeClientWithTLS(t, node, user, map[string]any{
			"enabled": true, "insecure": true, "server_name": "localhost", "alpn": []string{"http/1.1"},
		})
	}
	return runtimeClient(t, node, user)
}
