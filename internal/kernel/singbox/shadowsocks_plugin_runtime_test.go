package singbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestShadowsocks2022PluginRuntime(t *testing.T) {
	for _, cipher := range []string{"shadowsocks2022-128", "shadowsocks2022-256"} {
		for _, mode := range []string{"ws", "wss"} {
			t.Run(cipher+"/"+mode, func(t *testing.T) {
				node := runtimeNode(t, cipher)
				node.Plugin, node.PluginOpt, node.Network = "v2ray-plugin", "mode=websocket;mux=0", "ws"
				node.NetworkSettings = map[string]any{"path": "/ss-plugin", "host": "localhost"}
				cert := runtimeCertificate(t)
				pluginOptions := node.PluginOpt + ";host=localhost;path=/ss-plugin"
				if mode == "wss" {
					node.TLS = 1
					node.TLSSettings = map[string]any{"server_name": "localhost"}
					block, _ := pem.Decode(cert.CertPEM)
					pluginOptions += ";tls;certRaw=" + base64.StdEncoding.EncodeToString(block.Bytes)
				}
				user := runtimeUser(t, 712)
				keeper := runtimeUser(t, 713)
				target := runtimeEcho(t)
				core := relayStartCore(t, "xray", node, []model.UserSpec{user, keeper}, cert, target.String())
				// 测试出站将文档地址转入回环夹具，避免触发节点的私网目的地址拦截。
				destination := singM.ParseSocksaddr("198.51.100.10:80")
				client := shadowsocksPluginClient(t, node, user, pluginOptions)
				for range 2 {
					conn := runtimeDial(t, client, destination)
					runtimeExchange(t, conn)
					conn.Close()
				}
				if removed, err := core.RemoveUsers([]model.UserSpec{user}); err != nil || removed != 1 {
					t.Fatalf("撤销插件用户失败：removed=%d error=%v", removed, err)
				}
				runtimeReject(t, client, destination)
				if added, err := core.AddUsers([]model.UserSpec{user}); err != nil || added != 1 {
					t.Fatalf("恢复插件用户失败：added=%d error=%v", added, err)
				}
				conn := runtimeDial(t, client, destination)
				runtimeExchange(t, conn)
				conn.Close()
			})
		}
	}
}

func shadowsocksPluginClient(t *testing.T, node *model.NodeSpec, user model.UserSpec, pluginOptions string) adapter.Outbound {
	t.Helper()
	size := 16
	if node.Cipher == "2022-blake3-aes-256-gcm" {
		size = 32
	}
	outbound := map[string]any{
		"type": "shadowsocks", "tag": "probe", "server": "127.0.0.1", "server_port": node.ServerPort,
		"method": node.Cipher, "password": node.ServerKey + ":" + base64.StdEncoding.EncodeToString([]byte(user.UUID)[:size]),
		"plugin": "v2ray-plugin", "plugin_opts": pluginOptions,
	}
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{outbound}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	return client.Outbound().Default()
}
