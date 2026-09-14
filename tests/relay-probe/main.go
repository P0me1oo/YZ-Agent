// relay-probe 用于两台独立测试机的中转验收，不安装服务，也不读取现有 Node 配置。
package main

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
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
	xrayLog "github.com/xtls/xray-core/common/log"
)

const payload = "relay-cross-host-upload-payload"

type landing struct {
	Kernel     string
	Protocol   string
	Port       int
	Credential string
}

type testConfig struct {
	Address     string
	Certificate string
	PrivateKey  string
	Landings    []landing
}

func main() {
	mode := flag.String("mode", "", "prepare、landing 或 probe")
	file := flag.String("config", "", "本次测试的独立配置文件")
	address := flag.String("address", "", "落地测试机地址，仅 prepare 使用")
	basePort := flag.Int("base-port", 0, "四个已确认可用的连续测试端口，仅 prepare 使用")
	entryKernel := flag.String("entry-kernel", "singbox", "入口内核")
	flag.Parse()
	if *file == "" {
		fatal(fmt.Errorf("必须指定独立测试配置路径"))
	}
	if *mode == "prepare" {
		fatal(prepare(*file, *address, *basePort))
		return
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		fatal(err)
	}
	var cfg testConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		fatal(err)
	}
	if net.ParseIP(cfg.Address) == nil || len(cfg.Landings) != 4 {
		fatal(fmt.Errorf("测试拓扑无效"))
	}
	switch *mode {
	case "landing":
		fatal(runLandings(*file, cfg))
	case "probe":
		fatal(runProbe(*file, cfg, *entryKernel))
	default:
		fatal(fmt.Errorf("无效测试模式"))
	}
}

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newUUID() string {
	id, err := uuid.NewV4()
	if err != nil {
		panic(err)
	}
	return id.String()
}

func prepare(file, address string, port int) error {
	if net.ParseIP(address) == nil || port < 1024 || port > 65531 {
		return fmt.Errorf("需要有效的测试地址和端口")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "relay-test.invalid"}, DNSNames: []string{"relay-test.invalid"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	cfg := testConfig{Address: address, Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))}
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"shadowsocks", "vless"} {
			credential := newUUID()
			if protocol == "shadowsocks" {
				raw := make([]byte, 16)
				if _, err := rand.Read(raw); err != nil {
					return err
				}
				credential = base64.StdEncoding.EncodeToString(raw)
			} else {
				credential = credential[:14] + "0000" + credential[18:]
			}
			cfg.Landings = append(cfg.Landings, landing{Kernel: kind, Protocol: protocol, Port: port + len(cfg.Landings), Credential: credential})
		}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer output.Close()
	if _, err := output.Write(data); err != nil {
		return err
	}
	fmt.Println("已生成独立测试配置；认证信息不输出到终端")
	return nil
}

func tlsCert(cfg testConfig) kernel.TLSCert {
	return kernel.TLSCert{CertPEM: []byte(cfg.Certificate), KeyPEM: []byte(cfg.PrivateKey)}
}

func newCore(kind, dir string, custom []map[string]any) (kernel.Kernel, error) {
	cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: dir, CustomOutbound: custom}
	switch kind {
	case "singbox":
		return singbox.New(cfg), nil
	case "xray":
		return xray.New(cfg), nil
	}
	return nil, fmt.Errorf("不支持该测试内核")
}

func runLandings(file string, cfg testConfig) error {
	var cores []kernel.Kernel
	defer func() {
		for _, core := range cores {
			core.Stop()
		}
	}()
	for i, item := range cfg.Landings {
		marker := item.Kernel + "-" + item.Protocol
		target, closeEcho, err := markerEcho(marker)
		if err != nil {
			return err
		}
		defer closeEcho()
		node := &model.NodeSpec{Protocol: item.Protocol, ListenIP: "0.0.0.0", ServerPort: item.Port, Network: "tcp", ServerName: "relay-test.invalid",
			Relay: &model.RelayConfig{Mode: "landing", Protocol: item.Protocol}}
		if item.Protocol == "shadowsocks" {
			node.Relay.Cipher = "2022-blake3-aes-128-gcm"
			node.Relay.Password = item.Credential
		} else {
			node.TLS = 1
			node.Relay.VLESS = &model.RelayVLESSConfig{ID: item.Credential}
		}
		_, portText, _ := net.SplitHostPort(target)
		port, _ := strconv.Atoi(portText)
		var custom []map[string]any
		if item.Kernel == "singbox" {
			node.CustomRoutes = []map[string]any{{"ip_cidr": []string{"198.51.100.10/32"}, "action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": port}, {"action": "reject"}}
		} else {
			node.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"198.51.100.10/32"}, "outboundTag": "direct"}, {"type": "field", "network": "tcp,udp", "outboundTag": "block"}}
			custom = []map[string]any{{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"redirect": target,
				"finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}, "port": portText}}}}}
		}
		dir := filepath.Join(filepath.Dir(file), fmt.Sprintf("landing-%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		core, err := newCore(item.Kernel, dir, custom)
		if err != nil {
			return err
		}
		cores = append(cores, core)
		if err := core.Start(node, nil, tlsCert(cfg)); err != nil {
			return fmt.Errorf("启动 %s 落地失败: %w", marker, err)
		}
		quietXrayLogs()
	}
	if err := os.WriteFile(file+".ready", []byte("ready\n"), 0600); err != nil {
		return err
	}
	fmt.Println("四个独立落地已就绪，最长运行 20 分钟")
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return fmt.Errorf("落地测试超时")
		case <-ticker.C:
			if _, err := os.Stat(file + ".stop"); err == nil {
				for _, core := range cores {
					data, _, _, err := core.GetUserTraffic(context.Background())
					if err != nil || len(data) != 0 {
						return fmt.Errorf("落地重复上报用户流量")
					}
				}
				fmt.Println("落地用户计数为空，测试实例正常停止")
				return nil
			}
		}
	}
}

func runProbe(file string, cfg testConfig, kind string) error {
	// 信任仅限本次进程和临时证书文件，不修改系统证书库。
	caFile := file + ".ca.pem"
	if err := os.WriteFile(caFile, []byte(cfg.Certificate), 0600); err != nil {
		return err
	}
	if err := os.Setenv("SSL_CERT_FILE", caFile); err != nil {
		return err
	}
	var failures []error
	for _, protocol := range []string{"vless", "hysteria"} {
		if err := probeProtocol(file, cfg, kind, protocol); err != nil {
			failures = append(failures, fmt.Errorf("%s/%s: %w", kind, protocol, err))
		}
	}
	return errors.Join(failures...)
}

func probeProtocol(file string, cfg testConfig, kind, protocol string) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	node := &model.NodeSpec{Protocol: protocol, Version: 2, ListenIP: "127.0.0.1", ServerPort: port, Network: "tcp", TLS: 1, ServerName: "relay-test.invalid", UpMbps: 100, DownMbps: 100,
		Relay: &model.RelayConfig{Mode: "entry", RouteID: 100}}
	for i, item := range cfg.Landings {
		if kind == "xray" && item.Kernel != "singbox" {
			continue
		}
		child := model.RelayChild{NodeID: i + 1, Tag: fmt.Sprintf("relay-%d", i+1), RouteID: 101 + i, Protocol: item.Protocol, Address: cfg.Address, Port: item.Port}
		if item.Protocol == "shadowsocks" {
			child.Cipher = "2022-blake3-aes-128-gcm"
			child.Password = item.Credential
		} else {
			child.VLESS = &model.RelayVLESSConfig{ID: item.Credential, Network: "tcp", TLS: 1, Encryption: "none", TLSSettings: map[string]any{"server_name": "relay-test.invalid"}}
		}
		node.Relay.Children = append(node.Relay.Children, child)
	}
	dir := filepath.Join(filepath.Dir(file), kind+"-"+protocol+"-entry")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	core, err := newCore(kind, dir, nil)
	if err != nil {
		return err
	}
	defer core.Stop()
	first := model.UserSpec{ID: 8101, UUID: newUUID()}
	second := model.UserSpec{ID: 8102, UUID: newUUID()}
	if err := core.Start(node, []model.UserSpec{first}, tlsCert(cfg)); err != nil {
		return err
	}
	quietXrayLogs()
	want := map[int][2]int64{}
	wantLines := map[int]map[int][2]int64{}
	transfers := 0
	exchange := func(user model.UserSpec, child model.RelayChild, network string) error {
		item := cfg.Landings[child.NodeID-1]
		marker := item.Kernel + "-" + item.Protocol
		credential := user.UUID[:14] + fmt.Sprintf("%04x", child.RouteID) + user.UUID[18:]
		client, outbound, err := newClient(node, cfg, credential)
		if err != nil {
			return err
		}
		defer client.Close()
		if err := markerExchange(outbound, marker, network); err != nil {
			return fmt.Errorf("%s/%s 到 %s 的 %s 转发失败: %w", kind, protocol, marker, network, err)
		}
		delta := [2]int64{int64(len(payload)), int64(len(marker) + 1 + len(payload))}
		v := want[user.ID]
		want[user.ID] = [2]int64{v[0] + delta[0], v[1] + delta[1]}
		if wantLines[user.ID] == nil {
			wantLines[user.ID] = map[int][2]int64{}
		}
		v = wantLines[user.ID][child.NodeID]
		wantLines[user.ID][child.NodeID] = [2]int64{v[0] + delta[0], v[1] + delta[1]}
		transfers++
		return nil
	}
	for _, child := range node.Relay.Children {
		for _, network := range []string{"tcp", "udp"} {
			if err := exchange(first, child, network); err != nil {
				return err
			}
		}
	}
	for i := 0; i < 2; i++ {
		n, err := core.AddUsers([]model.UserSpec{second})
		if err != nil || n != 1-i {
			return fmt.Errorf("用户添加或重复添加失败: %d %v", n, err)
		}
	}
	for _, child := range node.Relay.Children {
		for _, network := range []string{"tcp", "udp"} {
			if err := exchange(second, child, network); err != nil {
				return err
			}
		}
	}
	if _, err := core.RemoveUsers([]model.UserSpec{first}); err != nil {
		return err
	}
	credential := first.UUID[:14] + fmt.Sprintf("%04x", node.Relay.Children[0].RouteID) + first.UUID[18:]
	removedClient, removedOutbound, err := newClient(node, cfg, credential)
	if err != nil {
		return err
	}
	err = markerReject(removedOutbound)
	_ = removedClient.Close()
	if err != nil {
		return fmt.Errorf("删除用户后的认证检查失败: %w", err)
	}
	if err := exchange(second, node.Relay.Children[0], "tcp"); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if err := core.Reload(node, []model.UserSpec{second}, tlsCert(cfg)); err != nil {
			return err
		}
		quietXrayLogs()
	}
	if err := exchange(second, node.Relay.Children[0], "tcp"); err != nil {
		return err
	}
	core.Stop()
	if err := core.Start(node, []model.UserSpec{second}, tlsCert(cfg)); err != nil {
		return err
	}
	quietXrayLogs()
	if err := exchange(second, node.Relay.Children[0], "udp"); err != nil {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _, _, err := core.GetUserTraffic(context.Background())
		if err != nil {
			return err
		}
		match := true
		for uid, value := range want {
			if got[uid] != value {
				match = false
			}
		}
		if match {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("跨重启用户计数不一致: got=%v want=%v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	lines, err := core.(kernel.RelayUserTrafficReader).GetRelayUserTraffic(context.Background())
	if err != nil {
		return err
	}
	for uid, nodes := range wantLines {
		for id, value := range nodes {
			if lines[uid][id] != value {
				return fmt.Errorf("用户线路计数不一致: uid=%d node=%d", uid, id)
			}
		}
	}
	totals, err := core.(kernel.RelayTrafficReader).GetRelayTraffic(context.Background())
	if err != nil {
		return err
	}
	for _, child := range node.Relay.Children {
		var value [2]int64
		for _, nodes := range wantLines {
			value[0] += nodes[child.NodeID][0]
			value[1] += nodes[child.NodeID][1]
		}
		if totals[child.NodeID][0] <= 0 || totals[child.NodeID][1] <= 0 {
			return fmt.Errorf("落地双向计数缺失: kernel=%s protocol=%s node=%d counts=%v", kind, protocol, child.NodeID, totals[child.NodeID])
		}
		if kind == "singbox" && totals[child.NodeID] != value {
			return fmt.Errorf("sing-box 落地有效载荷计数不一致")
		}
	}
	result := map[string]any{"entry_kernel": kind, "entry_protocol": protocol, "landings": len(node.Relay.Children), "transfers": transfers, "user_traffic": want, "relay_traffic": totals, "status": "passed"}
	return json.NewEncoder(os.Stdout).Encode(result)
}

type discardedXrayLogs struct{}

func (discardedXrayLogs) Handle(xrayLog.Message) {}

func quietXrayLogs() {
	// 原始拒绝日志可能含认证值；测试进程只输出探针结果与返回错误。
	xrayLog.RegisterHandler(discardedXrayLogs{})
}

func newClient(node *model.NodeSpec, cfg testConfig, credential string) (*box.Box, adapter.Outbound, error) {
	protocol := node.Protocol
	if protocol == "hysteria" {
		protocol = "hysteria2"
	}
	out := map[string]any{"type": protocol, "tag": "probe", "server": "127.0.0.1", "server_port": node.ServerPort,
		"tls": map[string]any{"enabled": true, "server_name": "relay-test.invalid", "certificate": []string{cfg.Certificate}}}
	if protocol == "vless" {
		out["uuid"] = credential
		out["packet_encoding"] = "xudp"
	} else {
		out["password"] = credential
	}
	data, err := json.Marshal(map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{out}})
	if err != nil {
		return nil, nil, err
	}
	ctx := include.Context(context.Background())
	opts, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return nil, nil, err
	}
	client, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return nil, nil, err
	}
	if err := client.Start(); err != nil {
		client.Close()
		return nil, nil, err
	}
	return client, client.Outbound().Default(), nil
}

func markerExchange(outbound adapter.Outbound, marker, network string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	target := singM.ParseSocksaddr("198.51.100.10:80")
	want := []byte(marker + ":" + payload)
	if network == "tcp" {
		conn, err := outbound.DialContext(ctx, "tcp", target)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
		if _, err := io.WriteString(conn, payload); err != nil {
			return err
		}
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("TCP 出口标记不匹配")
		}
	} else {
		conn, err := outbound.ListenPacket(ctx, target)
		if err != nil {
			return err
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
			return err
		}
		if _, err := conn.WriteTo([]byte(payload), target.UDPAddr()); err != nil {
			return err
		}
		got := make([]byte, 65535)
		n, _, err := conn.ReadFrom(got)
		if err != nil {
			return err
		}
		if !bytes.Equal(got[:n], want) {
			return fmt.Errorf("UDP 出口标记不匹配")
		}
	}
	return nil
}

func markerReject(outbound adapter.Outbound) error {
	checkError := func(err error) error {
		if err != nil && strings.Contains(err.Error(), "network changed") {
			return fmt.Errorf("网络监控未稳定，不能判断认证是否被拒绝: %w", err)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := outbound.DialContext(ctx, "tcp", singM.ParseSocksaddr("198.51.100.10:80"))
	if err != nil {
		return checkError(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, payload); err != nil {
		return checkError(err)
	}
	var response [1]byte
	n, err := io.ReadFull(conn, response[:])
	if n > 0 {
		return fmt.Errorf("已删除用户仍收到出口应答")
	}
	return checkError(err)
}

func markerEcho(marker string) (string, func(), error) {
	tcp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	udp, err := net.ListenPacket("udp4", tcp.Addr().String())
	if err != nil {
		tcp.Close()
		return "", nil, err
	}
	closeAll := func() { tcp.Close(); udp.Close() }
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, len(payload))
				for {
					if _, err := io.ReadFull(conn, buffer); err != nil {
						return
					}
					if _, err := io.WriteString(conn, marker+":"+string(buffer)); err != nil {
						return
					}
				}
			}()
		}
	}()
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, addr, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			if strings.EqualFold(string(buffer[:n]), payload) {
				_, _ = udp.WriteTo(append([]byte(marker+":"), buffer[:n]...), addr)
			}
		}
	}()
	return tcp.Addr().String(), closeAll, nil
}
