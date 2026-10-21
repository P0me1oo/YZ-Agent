//go:build with_quic && with_utls

package singbox

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/realityrelay"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/testing/realitytest"
	xreality "github.com/xtls/xray-core/transport/internet/reality"
)

// 客户端的 SNI 和入口端口保持相同，只变更 shortId，验证真实 REALITY/VLESS 双核心落地。
func TestRealityShortIDRelayRuntime(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, flow := range []string{"", "xtls-rprx-vision"} {
			t.Run(kind+"/"+flow, func(t *testing.T) {
				fixture := realitytest.Start(t, true)
				private := base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey.Bytes())
				t.Setenv("YZ_REALITY_RUNTIME_TEST_KEY", private)
				var secondID [8]byte
				if _, err := rand.Read(secondID[:]); err != nil {
					t.Fatal(err)
				}
				ids := [][8]byte{fixture.ShortID, secondID}
				user := runtimeUser(t, 771)
				cfg := realityrelay.Config{Listen: "127.0.0.1:0", ServerNames: []string{"reality.test"}, Keys: []realityrelay.KeyConfig{{Name: "shared", PrivateKeyEnv: "YZ_REALITY_RUNTIME_TEST_KEY"}}}
				var cores []kernel.Kernel
				var nodes []*model.NodeSpec
				for i, name := range []string{"first", "second"} {
					node := runtimeNode(t, "vless")
					node.TLS = 2
					node.Flow = flow
					node.TLSSettings = map[string]any{"server_name": "reality.test", "private_key": private, "short_id": hex.EncodeToString(ids[i][:]), "dest": fixture.Config.Dest}
					core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, name))
					cores = append(cores, core)
					nodes = append(nodes, node)
					cfg.Routes = append(cfg.Routes, realityrelay.RouteConfig{Name: name, Key: "shared", ShortID: hex.EncodeToString(ids[i][:]), Target: net.JoinHostPort("127.0.0.1", strconv.Itoa(node.ServerPort))})
				}
				server, err := realityrelay.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- server.Serve(context.Background(), listener) }()
				t.Cleanup(func() {
					_ = server.Close()
					select {
					case err := <-done:
						if err != nil {
							t.Error(err)
						}
					case <-time.After(5 * time.Second):
						t.Error("实验入口没有停止")
					}
				})
				clients := make([]adapter.Outbound, 0, 2)
				for i, name := range []string{"first", "second"} {
					client := shortIDRealityClient(t, listener.Addr().(*net.TCPAddr).Port, user, fixture.PublicKey, ids[i], flow)
					clients = append(clients, client)
					for _, network := range []string{"tcp", "udp"} {
						relayMarkerExchange(t, client, name, network)
					}
				}
				// 入口配置重复加载不改变线路；落地仍负责实际用户授权和撤销。
				if err := server.Reload(cfg); err != nil {
					t.Fatal(err)
				}
				if _, err := cores[0].RemoveUsers([]model.UserSpec{user}); err != nil {
					t.Fatal(err)
				}
				relayMarkerReject(t, clients[0], "tcp")
				relayMarkerExchange(t, clients[1], "second", "tcp")
				// Xray 删除最后一名普通节点用户后按现有规则停止监听，恢复须走完整生命周期。
				if err := cores[0].Reload(nodes[0], []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				relayMarkerExchange(t, clients[0], "first", "tcp")
				if server.Stats().Rejected != 0 {
					t.Fatal("合法客户端握手在前置被误拒绝")
				}
			})
		}
	}
}

func TestRealityShortIDRelayXrayClient(t *testing.T) {
	for _, hybrid := range []bool{false, true} {
		t.Run(strconv.FormatBool(hybrid), func(t *testing.T) {
			fixture := realitytest.Start(t, hybrid)
			t.Setenv("YZ_REALITY_XRAY_CLIENT_TEST_KEY", base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey.Bytes()))
			cfg := realityrelay.Config{Listen: "127.0.0.1:0", ServerNames: []string{"reality.test"}, Keys: []realityrelay.KeyConfig{{Name: "shared", PrivateKeyEnv: "YZ_REALITY_XRAY_CLIENT_TEST_KEY"}}, Routes: []realityrelay.RouteConfig{{Name: "echo", Key: "shared", ShortID: hex.EncodeToString(fixture.ShortID[:]), Target: fixture.Address}}}
			server, err := realityrelay.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(context.Background(), listener) }()
			t.Cleanup(func() {
				_ = server.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("Xray 客户端实验入口没有停止")
				}
			})
			raw, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			secured, err := xreality.UClient(raw, &xreality.Config{ServerName: "reality.test", Fingerprint: "chrome", PublicKey: fixture.PublicKey, ShortId: fixture.ShortID[:]}, ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), xnet.Port(listener.Addr().(*net.TCPAddr).Port)))
			if err != nil {
				t.Fatal("Xray 客户端未完成穿过实验入口的 Reality 握手")
			}
			realitytest.Echo(t, secured)
			if server.Stats().Rejected != 0 {
				t.Fatal("Xray 客户端认证被误拒绝")
			}
		})
	}
}

func shortIDRealityClient(t *testing.T, port int, user model.UserSpec, public []byte, sid [8]byte, flow string) adapter.Outbound {
	t.Helper()
	opts := map[string]any{"type": "vless", "tag": "probe", "server": "127.0.0.1", "server_port": port, "uuid": user.UUID, "packet_encoding": "xudp", "connect_timeout": "5s",
		"tls": map[string]any{"enabled": true, "server_name": "reality.test", "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": base64.RawURLEncoding.EncodeToString(public), "short_id": hex.EncodeToString(sid[:])}}}
	if flow != "" {
		opts["flow"] = flow
	}
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{opts}})
	if err != nil {
		t.Fatal("构建独立客户端失败")
	}
	ctx := include.Context(context.Background())
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal("解析独立客户端配置失败")
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal("创建独立客户端失败")
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Start(); err != nil {
		t.Fatal("启动独立客户端失败")
	}
	return client.Outbound().Default()
}
