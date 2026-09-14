//go:build with_quic

package xray

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/google/uuid"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
	hysteriaaccount "github.com/xtls/xray-core/proxy/hysteria/account"
)

// 固定核心必须保留真实用户身份，同时按每个认证会话分别保存路由编号。
func TestHysteria2RelayAuthentication(t *testing.T) {
	user := model.UserSpec{ID: 101, UUID: hy2TestUUID(t).String()}
	memoryUser, err := toMemoryUser("hysteria", &model.NodeSpec{Protocol: "hysteria", Version: 2}, user)
	if err != nil {
		t.Fatal(err)
	}
	validator := hysteriaaccount.NewValidator()
	if err := validator.Add(memoryUser); err != nil {
		t.Fatal(err)
	}
	var sessions []*hysteriaaccount.MemoryAccount
	for _, route := range []uint16{1, 12, 65535} {
		auth := hy2RouteAuth(t, user, route)
		resolved := validator.Get(auth)
		if resolved == nil || resolved.Email != userEmail(user.ID) {
			t.Fatal("路由认证没有恢复原用户")
		}
		account := resolved.Account.(*hysteriaaccount.MemoryAccount)
		if uint16(account.VR) != route {
			t.Fatalf("认证路由 = %d，期望 %d", account.VR, route)
		}
		sessions = append(sessions, account)
	}
	if sessions[0].VR != 1 {
		t.Fatal("后续认证修改了旧会话路由")
	}
	unknown := model.UserSpec{ID: 102, UUID: hy2TestUUID(t).String()}
	if validator.Get(hy2RouteAuth(t, unknown, 12)) != nil {
		t.Fatal("未知用户通过了路由认证")
	}
	if err := validator.DelByEmail(userEmail(user.ID)); err != nil {
		t.Fatal(err)
	}
	if validator.Get(hy2RouteAuth(t, user, 12)) != nil {
		t.Fatal("删除用户后新认证仍被接受")
	}
}

// 所有监听都在回环地址，凭据和证书仅在内存中生成。
// 三个出口返回不同标记，验证原生 HY2 客户端确实按认证编号选择落地。
func TestHysteria2RelayRuntime(t *testing.T) {
	for _, obfs := range []bool{false, true} {
		t.Run(fmt.Sprintf("salamander=%t", obfs), func(t *testing.T) {
			cert := hy2TestCertificate(t)
			first := model.UserSpec{ID: 101, UUID: hy2TestUUID(t).String()}
			second := model.UserSpec{ID: 102, UUID: hy2TestUUID(t).String()}
			ssKey := make([]byte, 16)
			if _, err := rand.Read(ssKey); err != nil {
				t.Fatal(err)
			}
			internalID := hy2TestUUID(t)
			internalID[6], internalID[7] = 0, 0
			ss := &model.NodeSpec{
				Protocol: "shadowsocks", ListenIP: "127.0.0.1", ServerPort: hy2TestPort(t, "tcp"),
				Cipher: "2022-blake3-aes-128-gcm",
				Relay: &model.RelayConfig{
					Mode: panel.RelayModeLanding, Protocol: "shadowsocks",
					Cipher: "2022-blake3-aes-128-gcm", Password: base64.StdEncoding.EncodeToString(ssKey),
				},
			}
			vless := &model.NodeSpec{
				Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: hy2TestPort(t, "tcp"),
				Network: "tcp", Decryption: "none",
				Relay: &model.RelayConfig{
					Mode: panel.RelayModeLanding, Protocol: "vless",
					VLESS: &model.RelayVLESSConfig{ID: internalID.String()},
				},
			}
			ssKernel := hy2StartKernel(t, ss, nil, kernel.TLSCert{}, hy2TestEcho(t, "ss"))
			vlessKernel := hy2StartKernel(t, vless, nil, kernel.TLSCert{}, hy2TestEcho(t, "vless"))
			node := &model.NodeSpec{
				Protocol: "hysteria", Version: 2, ListenIP: "127.0.0.1",
				ServerPort: hy2TestPort(t, "udp"), UpMbps: 100, DownMbps: 100,
				Relay: &model.RelayConfig{
					Mode: panel.RelayModeEntry, RouteID: 11,
					Children: []model.RelayChild{
						{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: ss.Protocol, Address: ss.ListenIP,
							Port: ss.ServerPort, Cipher: ss.Cipher, Password: ss.Relay.Password},
						{NodeID: 9, Tag: "relay-9", RouteID: 13, Protocol: vless.Protocol, Address: vless.ListenIP,
							Port: vless.ServerPort, VLESS: &model.RelayVLESSConfig{
								ID: internalID.String(), Network: "tcp", Encryption: "none",
							}},
					},
				},
			}
			if obfs {
				node.Obfs, node.ObfsPassword = "salamander", hy2TestUUID(t).String()
			}
			entry := hy2StartKernel(t, node, []model.UserSpec{first}, cert, hy2TestEcho(t, "direct"))
			var clients []*hy2TestClient
			client := func(user model.UserSpec, route uint16) *hy2TestClient {
				c := hy2NewClient(t, node, cert, hy2RouteAuth(t, user, route))
				clients = append(clients, c)
				return c
			}
			closeClients := func() {
				for _, c := range clients {
					c.close()
				}
				clients = nil
			}
			wantUsers := map[int][2]int64{}
			wantRoutes := map[int]map[int][2]int64{}
			exchange := func(c *hy2TestClient, userID, nodeID int, marker, network string) {
				t.Helper()
				if err := hy2TestExchange(c.outbound, marker, network); err != nil {
					t.Fatalf("%s 经 %s 落地失败: %v", network, marker, err)
				}
				delta := [2]int64{int64(len(hy2TestPayload)), int64(len(marker) + 1 + len(hy2TestPayload))}
				total := wantUsers[userID]
				wantUsers[userID] = [2]int64{total[0] + delta[0], total[1] + delta[1]}
				if nodeID != 0 {
					if wantRoutes[userID] == nil {
						wantRoutes[userID] = map[int][2]int64{}
					}
					total = wantRoutes[userID][nodeID]
					wantRoutes[userID][nodeID] = [2]int64{total[0] + delta[0], total[1] + delta[1]}
				}
			}
			direct, firstSS, firstVLESS := client(first, 11), client(first, 12), client(first, 13)
			for _, network := range []string{"tcp", "udp"} {
				exchange(direct, first.ID, 0, "direct", network)
				exchange(firstSS, first.ID, 7, "ss", network)
				exchange(firstVLESS, first.ID, 9, "vless", network)
			}
			// 同一用户同时持有不同路由的 QUIC 会话，复用旧会话时不能串线。
			exchange(firstSS, first.ID, 7, "ss", "tcp")
			hy2AssertTraffic(t, entry, wantUsers, wantRoutes)
			if err := hy2TestExchange(client(second, 12).outbound, "ss", "tcp"); err == nil {
				t.Fatal("未知用户被入口接受")
			}
			for _, want := range []int{1, 0} {
				if added, err := entry.AddUsers([]model.UserSpec{second}); err != nil || added != want {
					t.Fatalf("添加用户: added=%d, err=%v", added, err)
				}
			}
			secondSS := client(second, 12)
			exchange(secondSS, second.ID, 7, "ss", "tcp")
			for _, want := range []int{1, 0} {
				if removed, err := entry.RemoveUsers([]model.UserSpec{first}); err != nil || removed != want {
					t.Fatalf("删除用户: removed=%d, err=%v", removed, err)
				}
			}
			if err := hy2TestExchange(client(first, 13).outbound, "vless", "tcp"); err == nil {
				t.Fatal("被删除用户的新会话仍能连接")
			}
			exchange(secondSS, second.ID, 7, "ss", "udp")
			third := model.UserSpec{ID: 103, UUID: hy2TestUUID(t).String()}
			allUsers := []model.UserSpec{second, third}
			if added, removed, err := entry.UpdateUsers(allUsers); err != nil || added != 1 || removed != 0 {
				t.Fatalf("全量添加用户: added=%d, removed=%d, err=%v", added, removed, err)
			}
			if err := entry.Reload(node, allUsers, cert); err != nil {
				t.Fatalf("全量用户同步后的配置重载失败: %v", err)
			}
			exchange(secondSS, second.ID, 7, "ss", "tcp")
			if added, removed, err := entry.UpdateUsers([]model.UserSpec{second}); err != nil || added != 0 || removed != 1 {
				t.Fatalf("全量删除用户: added=%d, removed=%d, err=%v", added, removed, err)
			}
			for range 2 {
				if err := entry.Reload(node, []model.UserSpec{second}, cert); err != nil {
					t.Fatalf("重复同步失败: %v", err)
				}
				exchange(secondSS, second.ID, 7, "ss", "tcp")
			}
			closeClients()
			next := *node
			next.ServerPort = hy2TestPort(t, "udp")
			if err := entry.Reload(&next, []model.UserSpec{second}, cert); err != nil {
				t.Fatalf("入口变更端口后重载失败: %v", err)
			}
			node = &next
			exchange(client(second, 13), second.ID, 9, "vless", "tcp")
			exchange(client(second, 13), second.ID, 9, "vless", "udp")
			hy2AssertTraffic(t, entry, wantUsers, wantRoutes)
			closeClients()
			entry.Stop()
			hy2AssertTraffic(t, entry, wantUsers, wantRoutes)
			if err := entry.Start(node, []model.UserSpec{second}, cert); err != nil {
				t.Fatalf("停止后恢复失败: %v", err)
			}
			exchange(client(second, 12), second.ID, 7, "ss", "tcp")
			hy2AssertTraffic(t, entry, wantUsers, wantRoutes)
			closeClients()
			for _, landing := range []*Xray{ssKernel, vlessKernel} {
				traffic, _, _, err := landing.GetUserTraffic(context.Background())
				if err != nil || len(traffic) != 0 {
					t.Fatalf("落地重复统计了用户流量: %v, err=%v", traffic, err)
				}
			}
			relay, err := entry.GetRelayTraffic(context.Background())
			if err != nil || relay[7][0] == 0 || relay[7][1] == 0 || relay[9][0] == 0 || relay[9][1] == 0 {
				t.Fatalf("落地出站没有双向流量: %v, err=%v", relay, err)
			}
		})
	}
}

func hy2StartKernel(t *testing.T, node *model.NodeSpec, users []model.UserSpec, cert kernel.TLSCert, target string) *Xray {
	t.Helper()
	// 目标是文档保留地址，所有 direct 出站在本次测试内重定向到回环应答服务。
	// 保留真正生成的中转规则，因此能够验证认证编号实际选择了哪个落地。
	_, targetPort, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.KernelConfig{Type: "xray", LogLevel: "fatal", ConfigDir: t.TempDir(),
		CustomOutbound: []map[string]any{{
			"tag": "direct", "protocol": "freedom", "settings": map[string]any{
				"redirect": target,
				"finalRules": []map[string]any{{
					"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}, "port": targetPort,
				}},
			},
		}},
	}
	if err := model.ValidateNodeSpec(node, cfg); err != nil {
		t.Fatalf("配置校验失败: %v", err)
	}
	x := New(cfg)
	if err := x.Start(node, users, cert); err != nil {
		t.Fatalf("启动测试节点失败: %v", err)
	}
	t.Cleanup(x.Stop)
	return x
}

func hy2TestUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func hy2RouteAuth(t *testing.T, user model.UserSpec, route uint16) string {
	t.Helper()
	id, err := uuid.Parse(user.UUID)
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint16(id[6:8], route)
	return id.String()
}

func hy2TestPort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func hy2TestCertificate(t *testing.T) kernel.TLSCert {
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
		SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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

type hy2TestClient struct {
	outbound adapter.Outbound
	close    func()
}

func hy2NewClient(t *testing.T, node *model.NodeSpec, cert kernel.TLSCert, auth string) *hy2TestClient {
	t.Helper()
	outbound := map[string]any{
		"type": "hysteria2", "tag": "test", "server": "127.0.0.1", "server_port": node.ServerPort,
		"password": auth, "connect_timeout": "3s",
		"tls": map[string]any{
			"enabled": true, "server_name": "localhost", "certificate": []string{string(cert.CertPEM)},
		},
	}
	if node.Obfs != "" {
		outbound["obfs"] = map[string]any{"type": node.Obfs, "password": node.ObfsPassword}
	}
	data, err := json.Marshal(map[string]any{
		"log": map[string]any{"disabled": true}, "outbounds": []any{outbound},
	})
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
	var once sync.Once
	closeClient := func() { once.Do(func() { _ = client.Close() }) }
	t.Cleanup(closeClient)
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	return &hy2TestClient{outbound: client.Outbound().Default(), close: closeClient}
}

const hy2TestPayload = "hy2-relay-payload"

func hy2TestEcho(t *testing.T, marker string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	packets, err := net.ListenPacket("udp4", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = packets.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, len(hy2TestPayload))
				for {
					if _, err := io.ReadFull(conn, buffer); err != nil {
						return
					}
					if _, err := conn.Write(append([]byte(marker+":"), buffer...)); err != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		buffer := make([]byte, 2048)
		for {
			n, addr, err := packets.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = packets.WriteTo(append([]byte(marker+":"), buffer[:n]...), addr)
		}
	}()
	return target
}

func hy2TestExchange(client adapter.Outbound, marker, network string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	destination := singM.ParseSocksaddr("192.0.2.1:80")
	want := []byte(marker + ":" + hy2TestPayload)
	if network == "tcp" {
		conn, err := client.DialContext(ctx, "tcp", destination)
		if err != nil {
			return fmt.Errorf("连接 TCP 目标: %w", err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write([]byte(hy2TestPayload)); err != nil {
			return fmt.Errorf("发送 TCP 数据: %w", err)
		}
		response := make([]byte, len(want))
		if _, err := io.ReadFull(conn, response); err != nil {
			return fmt.Errorf("读取 TCP 回包: %w", err)
		}
		if !bytes.Equal(response, want) {
			return fmt.Errorf("TCP 出口标记不匹配")
		}
		return nil
	}
	conn, err := client.ListenPacket(ctx, destination)
	if err != nil {
		return err
	}
	defer conn.Close()
	timeout := time.AfterFunc(3*time.Second, func() { _ = conn.Close() })
	defer timeout.Stop()
	if _, err := conn.WriteTo([]byte(hy2TestPayload), destination.UDPAddr()); err != nil {
		return err
	}
	response := make([]byte, 65535)
	n, _, err := conn.ReadFrom(response)
	if err != nil {
		return err
	}
	if !bytes.Equal(response[:n], want) {
		return fmt.Errorf("UDP 出口标记不匹配")
	}
	return nil
}

func hy2AssertTraffic(t *testing.T, entry *Xray, wantUsers map[int][2]int64, wantRoutes map[int]map[int][2]int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		traffic, _, _, err := entry.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		routes, err := entry.GetRelayUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		matches := true
		for id, want := range wantUsers {
			matches = matches && traffic[id] == want
		}
		for userID, nodes := range wantRoutes {
			for nodeID, want := range nodes {
				matches = matches && routes[userID][nodeID] == want
			}
		}
		if matches {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("流量未按真实用户和落地累计一次: users=%v want=%v, routes=%v want=%v", traffic, wantUsers, routes, wantRoutes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
