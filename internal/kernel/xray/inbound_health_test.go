package xray

import (
	"bytes"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/app/proxyman"
	"github.com/xtls/xray-core/infra/conf/serial"
	"github.com/xtls/xray-core/transport/internet"
)

func TestVLESSInboundTCPHealthParsedByXray(t *testing.T) {
	for _, network := range []string{"", "tcp", "raw", "ws", "grpc", "httpupgrade", "xhttp"} {
		for _, entry := range []bool{false, true} {
			name := network
			if entry {
				name += "/relay-entry"
			}
			t.Run(name, func(t *testing.T) {
				node := &model.NodeSpec{Protocol: "vless", Network: network, ServerPort: 10086, AcceptProxyProtocol: true}
				if entry {
					node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11}
				}
				socket := parsedInboundSocket(t, node)
				if socket == nil || socket.TcpKeepAliveIdle != 30 || socket.TcpKeepAliveInterval != 10 || socket.TcpUserTimeout != 60000 {
					t.Fatal("Xray 实际解析的入口断线探测参数不正确，应为 30 秒、10 秒及 60000 毫秒")
				}
				if !socket.AcceptProxyProtocol {
					t.Fatal("原有 PROXY 头设置丢失")
				}
			})
		}
	}
}

func TestVLESSInboundHealthPreservesTransportAndIsolation(t *testing.T) {
	node := &model.NodeSpec{Protocol: "vless", Network: "ws", ServerPort: 10086,
		NetworkSettings: map[string]any{"path": "/health-test", "host": "health-test.invalid"}}
	first := buildInbound(testKernelCfg, node, testUsers, kernel.TLSCert{})
	second := buildInbound(testKernelCfg, node, testUsers, kernel.TLSCert{})
	stream := first["streamSettings"].(M)
	settings := stream["wsSettings"].(map[string]any)
	if settings["path"] != "/health-test" || settings["host"] != "health-test.invalid" {
		t.Fatal("添加断线探测不应改变传输参数")
	}
	socket, ok := stream["sockopt"].(M)
	if !ok {
		t.Fatal("未生成断线探测设置")
	}
	socket["tcpKeepAliveIdle"] = 99
	if second["streamSettings"].(M)["sockopt"].(M)["tcpKeepAliveIdle"] != 30 {
		t.Fatal("不同次生成的入口配置不应共享可变探测设置")
	}
	if _, changed := node.NetworkSettings["tcpKeepAliveIdle"]; changed {
		t.Fatal("不应修改主控提供的传输参数")
	}
}

func TestVLESSInboundHealthScope(t *testing.T) {
	for _, node := range []*model.NodeSpec{
		{Protocol: "vless", Network: "kcp", ServerPort: 10086},
		{Protocol: "vless", Network: "hysteria", ServerPort: 10086},
		{Protocol: "vmess", Network: "tcp", ServerPort: 10086},
		{Protocol: "vless", Network: "tcp", ServerPort: 10086, Relay: &model.RelayConfig{
			Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: testUsers[0].UUID},
		}},
	} {
		inbound := buildInbound(testKernelCfg, node, testUsers, kernel.TLSCert{})
		socket, _ := inbound["streamSettings"].(M)["sockopt"].(M)
		for _, key := range []string{"tcpKeepAliveIdle", "tcpKeepAliveInterval", "tcpUserTimeout"} {
			if _, exists := socket[key]; exists {
				t.Fatalf("断线探测不应扩展到本次范围外的入口：%s/%s", node.Protocol, node.Network)
			}
		}
	}
}

// 从最终配置经核心解析得到 socket 设置，避免只检查生成器中的字段。
func parsedInboundSocket(t *testing.T, node *model.NodeSpec) *internet.SocketConfig {
	t.Helper()
	data, err := marshalConfig(testKernelCfg, node, testUsers, kernel.TLSCert{})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := serial.LoadJSONConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := parsed.Inbound[0].ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	return receiver.(*proxyman.ReceiverConfig).GetStreamSettings().GetSocketSettings()
}
