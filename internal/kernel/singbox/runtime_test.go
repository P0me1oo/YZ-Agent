package singbox

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
)

// 凭据和证书仅在测试内存中生成；所有监听都使用回环地址。
func TestSingBoxRuntimeUserLifecycle(t *testing.T) {
	for _, protocol := range []string{"vmess", "vless", "trojan", "shadowsocks", "shadowsocks2022-128", "shadowsocks2022-256", "socks", "http", "anytls", "mieru", "mieru-udp"} {
		t.Run(protocol, func(t *testing.T) { testRuntimeUserLifecycle(t, protocol) })
	}
}

func testRuntimeUserLifecycle(t *testing.T, protocol string) {
	node := runtimeNode(t, protocol)
	cert := runtimeCertificate(t)
	first, second := runtimeUser(t, 101), runtimeUser(t, 102)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	if err := s.Start(node, []model.UserSpec{first}, cert); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	t.Cleanup(s.Stop)
	echo := runtimeEcho(t)
	firstClient := runtimeClient(t, node, first)
	firstConn := runtimeDial(t, firstClient, echo)
	runtimeExchange(t, firstConn)

	if added, err := s.AddUsers([]model.UserSpec{second}); err != nil || added != 1 {
		t.Fatalf("添加用户: added=%d, err=%v", added, err)
	}
	if added, err := s.AddUsers([]model.UserSpec{second}); err != nil || added != 0 {
		t.Fatalf("重复添加用户: added=%d, err=%v", added, err)
	}
	runtimeExchange(t, firstConn)
	secondClient := runtimeClient(t, node, second)
	secondConn := runtimeDial(t, secondClient, echo)
	runtimeExchange(t, secondConn)

	if removed, err := s.RemoveUsers([]model.UserSpec{first}); err != nil || removed != 1 {
		t.Fatalf("删除用户: removed=%d, err=%v", removed, err)
	}
	if removed, err := s.RemoveUsers([]model.UserSpec{first}); err != nil || removed != 0 {
		t.Fatalf("重复删除用户: removed=%d, err=%v", removed, err)
	}
	runtimeExchange(t, secondConn)
	// QUIC 等协议复用已认证会话；用户列表缩短后，新流仍必须归属同一用户。
	secondStream := runtimeDial(t, secondClient, echo)
	runtimeExchange(t, secondStream)
	runtimeReject(t, runtimeClient(t, node, first), echo)

	for range 2 {
		if err := s.Reload(node, []model.UserSpec{second}, cert); err != nil {
			t.Fatalf("重复重载失败: %v", err)
		}
		runtimeExchange(t, secondConn)
	}
	want := int64(5 * len(runtimePayload))
	runtimeWaitTraffic(t, s, second.ID, [2]int64{want, want})
	if removed, err := s.RemoveUsers([]model.UserSpec{second}); err != nil || removed != 1 {
		t.Fatalf("删除最后一个用户: removed=%d, err=%v", removed, err)
	}
	runtimeReject(t, runtimeClient(t, node, second), echo)
	if protocol == "socks" || protocol == "http" {
		runtimeReject(t, runtimeClient(t, node, model.UserSpec{}), echo)
	}
	if added, err := s.AddUsers([]model.UserSpec{second}); err != nil || added != 1 {
		t.Fatalf("恢复空用户列表: added=%d, err=%v", added, err)
	}
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, second), echo))
	_ = firstConn.Close()
	_ = secondConn.Close()
	_ = secondStream.Close()
	s.Stop()
	if protocol == "mieru-udp" {
		probe, err := net.ListenPacket("udp4", net.JoinHostPort(node.ListenIP, strconv.Itoa(node.ServerPort)))
		if err != nil {
			t.Fatalf("停止后 UDP 监听仍被占用: %v", err)
		}
		_ = probe.Close()
	}
	if err := s.Start(node, []model.UserSpec{second}, cert); err != nil {
		t.Fatalf("停止后重新启动失败: %v", err)
	}
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, second), echo))
}

func TestSingBoxRuntimeRouteReloadRollback(t *testing.T) {
	node := runtimeNode(t, "socks")
	user := runtimeUser(t, 201)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	echo := runtimeEcho(t)
	client := runtimeClient(t, node, user)
	conn := runtimeDial(t, client, echo)
	runtimeExchange(t, conn)

	blocked := *node
	blocked.CustomRoutes = []map[string]any{{"ip_cidr": []string{"127.0.0.1/32"}, "action": "reject"}}
	if err := s.Reload(&blocked, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatalf("应用拒绝规则失败: %v", err)
	}
	runtimeExchange(t, conn)
	runtimeReject(t, client, echo)

	invalid := *node
	invalid.CustomRoutes = []map[string]any{{"rule_set": []string{"missing-test-rule-set"}, "outbound": "direct"}}
	for range 2 {
		if err := s.Reload(&invalid, []model.UserSpec{user}, kernel.TLSCert{}); err == nil {
			t.Fatal("不存在的规则集不应被接受")
		}
		if s.nodeConfig != &blocked {
			t.Fatal("失败的路由更新推进了 Node 配置状态")
		}
		runtimeReject(t, client, echo)
	}
	if err := s.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatalf("恢复原规则失败: %v", err)
	}
	runtimeExchange(t, runtimeDial(t, client, echo))
}

func TestSingBoxRuntimeMieruRejectsOccupiedPort(t *testing.T) {
	for _, protocol := range []string{"mieru", "mieru-udp"} {
		t.Run(protocol, func(t *testing.T) {
			node := runtimeNode(t, protocol)
			address := net.JoinHostPort(node.ListenIP, strconv.Itoa(node.ServerPort))
			var listener io.Closer
			var err error
			if protocol == "mieru-udp" {
				listener, err = net.ListenPacket("udp4", address)
			} else {
				listener, err = net.Listen("tcp4", address)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			t.Cleanup(s.Stop)
			if err := s.Start(node, []model.UserSpec{runtimeUser(t, 401)}, kernel.TLSCert{}); err == nil {
				t.Fatal("监听端口被占用时必须返回启动错误")
			}
		})
	}
}

func TestSingBoxRuntimeEmptyUsersRemainClosedAfterRestart(t *testing.T) {
	for _, protocol := range []string{"socks", "http", "shadowsocks", "shadowsocks2022-128"} {
		t.Run(protocol, func(t *testing.T) {
			node := runtimeNode(t, protocol)
			user := runtimeUser(t, 501)
			s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			t.Cleanup(s.Stop)
			if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			if removed, err := s.RemoveUsers([]model.UserSpec{user}); err != nil || removed != 1 {
				t.Fatalf("清空用户失败: removed=%d, err=%v", removed, err)
			}
			s.Stop()
			if err := s.Start(node, nil, kernel.TLSCert{}); err != nil {
				t.Fatalf("空用户列表重启失败: %v", err)
			}
			echo := runtimeEcho(t)
			runtimeReject(t, runtimeClient(t, node, user), echo)
			if protocol != "shadowsocks" {
				// 验证匿名代理和仅持有 SS2022 服务器密钥都不能绕过空用户表。
				runtimeReject(t, runtimeClient(t, node, model.UserSpec{}), echo)
			}
			if added, err := s.AddUsers([]model.UserSpec{user}); err != nil || added != 1 {
				t.Fatalf("恢复空用户列表失败: added=%d, err=%v", added, err)
			}
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
		})
	}
}

func runtimeNode(t *testing.T, protocol string) *model.NodeSpec {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	node := &model.NodeSpec{
		Protocol: protocol, ListenIP: "127.0.0.1", ServerPort: port,
		Cipher: "aes-128-gcm", Network: "tcp", Transport: "TCP",
		ServerName: "localhost", UpMbps: 100, DownMbps: 100,
		CustomRoutes: []map[string]any{{"ip_cidr": []string{"127.0.0.1/32"}, "outbound": "direct"}},
	}
	if protocol == "hysteria2" {
		node.Protocol, node.Version = "hysteria", 2
	}
	if protocol == "mieru-udp" {
		node.Protocol, node.Transport = "mieru", "UDP"
	}
	if strings.HasPrefix(protocol, "shadowsocks2022-") {
		size := 16
		if protocol == "shadowsocks2022-256" {
			size = 32
		}
		key := make([]byte, size)
		if _, err := rand.Read(key); err != nil {
			t.Fatal(err)
		}
		node.Protocol = "shadowsocks"
		node.Cipher = fmt.Sprintf("2022-blake3-aes-%d-gcm", size*8)
		node.ServerKey = base64.StdEncoding.EncodeToString(key)
	}
	return node
}

func runtimeUser(t *testing.T, id int) model.UserSpec {
	t.Helper()
	identity, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}
	return model.UserSpec{ID: id, UUID: identity.String()}
}

func runtimeCertificate(t *testing.T) kernel.TLSCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return kernel.TLSCert{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
}

func runtimeClient(t *testing.T, node *model.NodeSpec, user model.UserSpec) adapter.Outbound {
	return runtimeClientWithTLS(t, node, user, nil)
}

func runtimeClientWithTLS(t *testing.T, node *model.NodeSpec, user model.UserSpec, tlsOptions map[string]any) adapter.Outbound {
	t.Helper()
	protocol := node.Protocol
	if protocol == "hysteria" && node.Version == 2 {
		protocol = "hysteria2"
	}
	opts := map[string]any{"type": protocol, "tag": "probe", "server": "127.0.0.1", "server_port": node.ServerPort, "connect_timeout": "5s"}
	switch protocol {
	case "vmess":
		opts["uuid"], opts["security"] = user.UUID, "auto"
	case "vless":
		opts["uuid"] = user.UUID
	case "shadowsocks":
		opts["method"], opts["password"] = node.Cipher, user.UUID
		if strings.HasPrefix(node.Cipher, "2022-") {
			size := 16
			if strings.Contains(node.Cipher, "256") {
				size = 32
			}
			opts["password"] = node.ServerKey
			if user.UUID != "" {
				opts["password"] = node.ServerKey + ":" + base64.StdEncoding.EncodeToString([]byte(user.UUID)[:size])
			}
		}
	case "tuic":
		opts["uuid"], opts["password"], opts["congestion_control"] = user.UUID, user.UUID, "cubic"
	case "hysteria":
		opts["auth_str"], opts["up_mbps"], opts["down_mbps"] = user.UUID, 100, 100
	case "trojan", "hysteria2", "anytls":
		opts["password"] = user.UUID
	case "socks", "http", "mieru":
		opts["username"], opts["password"] = user.UUID, user.UUID
		if protocol == "mieru" {
			opts["transport"] = node.Transport
		}
	default:
		t.Fatalf("未定义测试客户端: %s", protocol)
	}
	if protocol == "trojan" || protocol == "anytls" || protocol == "hysteria" || protocol == "hysteria2" || protocol == "tuic" {
		opts["tls"] = map[string]any{"enabled": true, "insecure": true, "server_name": "localhost", "alpn": []string{"h3"}}
	}
	if tlsOptions != nil {
		opts["tls"] = tlsOptions
	}
	if protocol == "hysteria2" && node.Obfs != "" {
		opts["obfs"] = map[string]any{"type": node.Obfs, "password": node.ObfsPassword}
	}
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{opts}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := include.Context(context.Background())
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		t.Fatalf("解析客户端配置失败: %v", err)
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Start(); err != nil {
		t.Fatalf("启动客户端失败: %v", err)
	}
	outbound := client.Outbound().Default()
	if outbound == nil {
		t.Fatal("找不到测试出站")
	}
	return outbound
}

const runtimePayload = "sing-box-upgrade-echo"

func runtimeEcho(t *testing.T) singM.Socksaddr {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return singM.ParseSocksaddr(listener.Addr().String())
}

func runtimeDial(t *testing.T, client adapter.Outbound, destination singM.Socksaddr) net.Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := runtimeConnect(ctx, client, destination)
	if err != nil {
		t.Fatalf("连接测试目标失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func runtimeExchange(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := runtimeExchangeResult(conn); err != nil {
		t.Fatalf("代理收发失败: %v", err)
	}
}

func runtimeExchangeResult(conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, runtimePayload); err != nil {
		return err
	}
	response := make([]byte, len(runtimePayload))
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if !bytes.Equal(response, []byte(runtimePayload)) {
		return fmt.Errorf("echo payload mismatch: length %s", strconv.Itoa(len(response)))
	}
	return nil
}

func runtimeReject(t *testing.T, client adapter.Outbound, destination singM.Socksaddr) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := runtimeConnect(ctx, client, destination)
	if err != nil {
		if strings.Contains(err.Error(), "network changed") {
			t.Fatalf("网络监控未稳定，不能据此判断认证或路由被拒绝: %v", err)
		}
		return
	}
	defer conn.Close()
	if err := runtimeExchangeResult(conn); err == nil {
		t.Fatal("应拒绝的用户或路由仍能传输流量")
	}
}

func runtimeConnect(ctx context.Context, client adapter.Outbound, destination singM.Socksaddr) (net.Conn, error) {
	return client.DialContext(ctx, "tcp", destination)
}

func runtimeWaitTraffic(t *testing.T, s *SingBox, userID int, want [2]int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		traffic, _, _, err := s.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := traffic[userID]
		if got == want {
			return
		}
		if got[0] > want[0] || got[1] > want[1] || time.Now().After(deadline) {
			t.Fatalf("重载前后的流量应恰好累计一次: got=%v, want=%v", got, want)
		}
		// 客户端读到数据时，服务端 write 返回后的计数回调可能尚未获得调度。
		time.Sleep(10 * time.Millisecond)
	}
}
