//go:build with_utls

// 独立隔离测试工具：只使用显式配置的临时身份，不读取面板和已有 Agent 配置。
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	coreSB "github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/realityrelay"
	"github.com/gofrs/uuid/v5"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	singM "github.com/sagernet/sing/common/metadata"
)

const serverName = "relay-perf.test"
const frontPort, landingPort, ssPort, internalPort, coverPort, dataPort = 28101, 28102, 28103, 28104, 28105, 28106

type perfConfig struct {
	Front          string `json:"front"`
	Landing        string `json:"landing"`
	PrivateKey     string `json:"private_key,omitempty"`
	PublicKey      string `json:"public_key"`
	ShortID        string `json:"short_id"`
	BaseUUID       string `json:"base_uuid,omitempty"`
	ClientUUID     string `json:"client_uuid"`
	InternalUUID   string `json:"internal_uuid,omitempty"`
	SSKey          string `json:"ss_key,omitempty"`
	HTTPToken      string `json:"http_token"`
	Certificate    []byte `json:"certificate"`
	CertificateKey []byte `json:"certificate_key,omitempty"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: yz-relay-perf init|front|landing|landing-core|target|client --config PATH")
	}
	f := flag.NewFlagSet("yz-relay-perf", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	path := f.String("config", "", "隔离配置路径")
	dir := f.String("dir", "", "配置生成目录或实例目录")
	front := f.String("front", "", "前置 IP")
	landing := f.String("landing", "", "落地 IP")
	mode := f.String("mode", "new", "new、buffered、old-ss、old-vless")
	direction := f.String("direction", "download", "download 或 upload")
	amount := f.Int64("bytes", 64<<20, "本轮有效载荷总量")
	concurrency := f.Int("concurrency", 1, "并行请求数")
	requests := f.Int("requests", 0, "请求总数，默认等于并行数")
	startAt := f.Float64("start-at", 0, "服务器 UTC Unix 开始时间")
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
		return errors.New("测试工具参数无效")
	}
	if args[0] == "init" {
		if *dir == "" || net.ParseIP(*front) == nil || net.ParseIP(*landing) == nil {
			return errors.New("init 需要 --dir、--front IP、--landing IP")
		}
		return generate(*dir, *front, *landing)
	}
	if *path == "" {
		return errors.New("必须显式指定隔离配置")
	}
	c, err := load(*path)
	if err != nil {
		return err
	}
	config.InitLogger(config.LogConfig{Level: "error", Output: "stderr"})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if args[0] == "client" {
		if *requests == 0 {
			*requests = *concurrency
		}
		if *startAt != 0 {
			delay := time.Until(time.Unix(0, int64(*startAt*1e9)))
			if delay < 0 || delay > time.Minute {
				return errors.New("开始时间必须在未来一分钟内")
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return runClient(ctx, c, *mode, *direction, *amount, *concurrency, *requests)
	}
	if *dir == "" {
		return errors.New("服务角色必须指定独立 --dir")
	}
	if err := os.MkdirAll(*dir, 0700); err != nil {
		return errors.New("无法创建隔离实例目录")
	}
	if args[0] == "front" {
		return runFront(ctx, c, *mode, *dir)
	}
	if args[0] == "landing" {
		return runLanding(ctx, c, *mode, *dir)
	}
	if args[0] == "landing-core" {
		return runLandingCore(ctx, c, *mode, *dir)
	}
	if args[0] == "target" {
		return runTargets(ctx, c, nil)
	}
	return errors.New("未知测试工具角色")
}

func load(path string) (perfConfig, error) {
	var c perfConfig
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("无法打开隔离配置")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 512<<10))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, errors.New("隔离配置格式无效")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("隔离配置含额外内容")
	}
	if net.ParseIP(c.Front) == nil || net.ParseIP(c.Landing) == nil || len(c.HTTPToken) != 32 {
		return c, errors.New("隔离配置中的地址或测试身份无效")
	}
	if _, err := uuid.FromString(c.ClientUUID); err != nil {
		return c, errors.New("隔离用户身份无效")
	}
	return c, nil
}

func generate(dir, front, landing string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, name := range []string{"front.json", "landing.json", "client.json", "realm.toml"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			return errors.New("目标目录已包含配置，拒绝覆盖")
		}
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	identity, err := uuid.NewV4()
	if err != nil {
		return err
	}
	internal, err := uuid.NewV4()
	if err != nil {
		return err
	}
	base := identity.String()
	base = base[:14] + "0000" + base[18:]
	user := base[:14] + "000c" + base[18:]
	internalID := internal.String()
	internalID = internalID[:14] + "0000" + internalID[18:]
	material := make([]byte, 40)
	if _, err := rand.Read(material); err != nil {
		return err
	}
	certificate, key, err := makeCertificate()
	if err != nil {
		return err
	}
	c := perfConfig{Front: front, Landing: landing, PrivateKey: base64.RawURLEncoding.EncodeToString(private.Bytes()), PublicKey: base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes()), ShortID: hex.EncodeToString(material[:8]), BaseUUID: base, ClientUUID: user, InternalUUID: internalID, SSKey: base64.StdEncoding.EncodeToString(material[8:24]), HTTPToken: hex.EncodeToString(material[24:]), Certificate: certificate, CertificateKey: key}
	write := func(name string, value any) error {
		b, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			return e
		}
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = f.Write(b)
		return e
	}
	if err := write("landing.json", c); err != nil {
		return err
	}
	c.CertificateKey = nil
	if err := write("front.json", c); err != nil {
		return err
	}
	c.PrivateKey = ""
	c.BaseUUID = ""
	c.InternalUUID = ""
	c.SSKey = ""
	if err := write("client.json", c); err != nil {
		return err
	}
	realm := fmt.Sprintf("[log]\nlevel = \"error\"\n\n[[endpoints]]\nlisten = \"0.0.0.0:%d\"\nremote = \"%s\"\n", frontPort, net.JoinHostPort(landing, strconv.Itoa(landingPort)))
	if err := os.WriteFile(filepath.Join(dir, "realm.toml"), []byte(realm), 0600); err != nil {
		return err
	}
	fmt.Println("已生成三份隔离配置及 Realm 配置；身份内容未输出")
	return nil
}

func makeCertificate() ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: serverName}, DNSNames: []string{serverName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, ExtraExtensions: []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 55555, 1}, Value: make([]byte, 1024)}}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	return der, encoded, err
}

func node(port int, c perfConfig) *model.NodeSpec {
	return &model.NodeSpec{Protocol: "vless", ListenIP: "0.0.0.0", ServerPort: port, Network: "tcp", TLS: 2, Flow: "xtls-rprx-vision", Decryption: "none", Host: c.Landing, TLSSettings: map[string]any{"private_key": c.PrivateKey, "server_name": serverName, "short_id": c.ShortID, "dest": net.JoinHostPort("127.0.0.1", strconv.Itoa(coverPort))}}
}

func runCore(n *model.NodeSpec, users []model.UserSpec, dir string) (*coreSB.SingBox, error) {
	k := config.KernelConfig{Type: "singbox", ConfigDir: dir, LogLevel: "fatal"}
	if err := model.ValidateNodeSpec(n, k); err != nil {
		return nil, errors.New("隔离节点配置校验失败")
	}
	core := coreSB.New(k)
	if err := core.Start(n, users, kernel.TLSCert{}); err != nil {
		return nil, errors.New("隔离核心启动失败，请检查监听端口和配置")
	}
	return core, nil
}

func runFront(ctx context.Context, c perfConfig, mode, dir string) error {
	if mode == "new" || mode == "buffered" {
		copyMode := "splice"
		if mode == "buffered" {
			copyMode = "buffered"
		}
		if err := os.Setenv("YZ_RELAY_PERF_PRIVATE_KEY", c.PrivateKey); err != nil {
			return err
		}
		s, err := realityrelay.New(realityrelay.Config{Listen: net.JoinHostPort("0.0.0.0", strconv.Itoa(frontPort)), ServerNames: []string{serverName}, Keys: []realityrelay.KeyConfig{{Name: "test", PrivateKeyEnv: "YZ_RELAY_PERF_PRIVATE_KEY"}}, Routes: []realityrelay.RouteConfig{{Name: "landing", Key: "test", ShortID: c.ShortID, Target: net.JoinHostPort(c.Landing, strconv.Itoa(landingPort))}}, CopyMode: copyMode, MaxConnections: 256, MaxHandshakes: 64})
		_ = os.Unsetenv("YZ_RELAY_PERF_PRIVATE_KEY")
		if err != nil {
			return errors.New("实验入口初始化失败")
		}
		defer s.Close()
		l, err := net.ListenTCP("tcp", &net.TCPAddr{Port: frontPort})
		if err != nil {
			return err
		}
		defer l.Close()
		emit(map[string]any{"event": "ready", "role": "front", "mode": mode})
		err = s.Serve(ctx, l)
		emit(map[string]any{"event": "relay_stats", "stats": s.Stats()})
		return err
	}
	if mode != "old-ss" && mode != "old-vless" {
		return errors.New("未知前置测试模式")
	}
	n, users := frontNode(c, mode)
	core, err := runCore(n, users, dir)
	if err != nil {
		return err
	}
	defer core.Stop()
	emit(map[string]any{"event": "ready", "role": "front", "mode": mode})
	<-ctx.Done()
	return nil
}

func frontNode(c perfConfig, mode string) (*model.NodeSpec, []model.UserSpec) {
	n := node(frontPort, c)
	n.Host = c.Front
	n.TLSSettings["dest"] = net.JoinHostPort(c.Landing, strconv.Itoa(coverPort))
	child := model.RelayChild{NodeID: 7, Tag: "relay-7", RouteID: 12, Address: c.Landing}
	if mode == "old-ss" {
		child.Protocol, child.Port, child.Cipher, child.Password = "shadowsocks", ssPort, "2022-blake3-aes-128-gcm", c.SSKey
	} else {
		child.Protocol, child.Port = "vless", internalPort
		child.VLESS = &model.RelayVLESSConfig{ID: c.InternalUUID, Network: "tcp", TLS: 2, Flow: "xtls-rprx-vision", Encryption: "none", RealitySettings: map[string]any{"server_name": serverName, "public_key": c.PublicKey, "short_id": c.ShortID, "fingerprint": "chrome"}}
	}
	n.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{child}}
	return n, []model.UserSpec{{ID: 1, UUID: c.BaseUUID, RelayRoutes: []int{12}}}
}

func runLanding(ctx context.Context, c perfConfig, mode, dir string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runTargets(ctx, c, ready) }()
	select {
	case err := <-done:
		return err
	case <-ready:
	}
	err := runLandingCore(ctx, c, mode, dir)
	cancel()
	return errors.Join(err, <-done)
}

func runTargets(ctx context.Context, c perfConfig, ready chan struct{}) error {
	parsed, err := x509.ParsePKCS8PrivateKey(c.CertificateKey)
	if err != nil {
		return errors.New("隔离证书无效")
	}
	certificate := tls.Certificate{Certificate: [][]byte{c.Certificate}, PrivateKey: parsed}
	cover, err := tls.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(coverPort)), &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		return err
	}
	defer cover.Close()
	var coverConnections sync.Map
	go func() {
		for {
			conn, err := cover.Accept()
			if err != nil {
				return
			}
			coverConnections.Store(conn, true)
			go func() {
				defer conn.Close()
				defer coverConnections.Delete(conn)
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	defer func() { coverConnections.Range(func(k, v any) bool { _ = k.(net.Conn).Close(); return true }) }()
	dataListener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(dataPort)))
	if err != nil {
		return err
	}
	defer dataListener.Close()
	dataServer := &http.Server{Handler: payloadHandler(c.HTTPToken), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}}
	defer dataServer.Close()
	go func() { _ = dataServer.Serve(tls.NewListener(dataListener, dataServer.TLSConfig)) }()
	if ready != nil {
		close(ready)
	} else {
		emit(map[string]any{"event": "ready", "role": "target"})
	}
	<-ctx.Done()
	return nil
}

func runLandingCore(ctx context.Context, c perfConfig, mode, dir string) error {
	if mode != "new" && mode != "buffered" && mode != "realm" && mode != "old-ss" && mode != "old-vless" {
		return errors.New("未知落地测试模式")
	}
	redirect := []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": dataPort}}
	n, users := landingNode(c, mode)
	n.CustomRoutes = redirect
	core, err := runCore(n, users, dir)
	if err != nil {
		return err
	}
	defer core.Stop()
	emit(map[string]any{"event": "ready", "role": "landing", "mode": mode})
	<-ctx.Done()
	return nil
}

// 每轮只启动该方案使用的落地核心，避免另外两套闲置核心影响内存对照。
func landingNode(c perfConfig, mode string) (*model.NodeSpec, []model.UserSpec) {
	if mode == "old-ss" {
		return &model.NodeSpec{Protocol: "shadowsocks", ListenIP: "0.0.0.0", ServerPort: ssPort, Cipher: "2022-blake3-aes-128-gcm", Relay: &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", ListenPort: ssPort, Cipher: "2022-blake3-aes-128-gcm", Password: c.SSKey}}, nil
	}
	if mode == "old-vless" {
		n := node(internalPort, c)
		n.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", ListenPort: internalPort, VLESS: &model.RelayVLESSConfig{ID: c.InternalUUID}}
		return n, nil
	}
	return node(landingPort, c), []model.UserSpec{{ID: 1, UUID: c.ClientUUID}}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func payloadHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/download" && r.Method == http.MethodGet {
			n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
			if err != nil || n < 1 || n > 512<<20 {
				http.Error(w, "invalid size", 400)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.CopyN(w, zeroReader{}, n)
			return
		}
		if r.URL.Path == "/upload" && r.Method == http.MethodPost {
			n, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 512<<20))
			if err != nil {
				http.Error(w, "incomplete", 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]int64{"received": n})
			return
		}
		http.NotFound(w, r)
	})
}

func runClient(ctx context.Context, c perfConfig, mode, direction string, amount int64, concurrency, requests int) error {
	if (direction != "download" && direction != "upload") || amount < 1 || amount > 512<<20 || concurrency < 1 || concurrency > 32 || requests < concurrency || requests > 4096 || amount < int64(requests) {
		return errors.New("测试量、方向或并行数超出范围")
	}
	optionsMap := map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{map[string]any{"type": "vless", "tag": "client", "server": c.Front, "server_port": frontPort, "uuid": c.ClientUUID, "flow": "xtls-rprx-vision", "connect_timeout": "10s", "tls": map[string]any{"enabled": true, "server_name": serverName, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}, "reality": map[string]any{"enabled": true, "public_key": c.PublicKey, "short_id": c.ShortID}}}}}
	data, _ := json.Marshal(optionsMap)
	clientCtx := include.Context(ctx)
	options, err := singJSON.UnmarshalExtendedContext[option.Options](clientCtx, data)
	if err != nil {
		return errors.New("客户端配置解析失败")
	}
	client, err := box.New(box.Options{Context: clientCtx, Options: options})
	if err != nil {
		return errors.New("客户端创建失败")
	}
	defer client.Close()
	if err := client.Start(); err != nil {
		return errors.New("客户端启动失败")
	}
	outbound := client.Outbound().Default()
	if outbound == nil {
		return errors.New("客户端出站缺失")
	}
	root, err := x509.ParseCertificate(c.Certificate)
	if err != nil {
		return errors.New("测试目标证书无效")
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	var connections atomic.Int64
	transport := &http.Transport{DisableKeepAlives: true, MaxConnsPerHost: concurrency, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 10 * time.Second, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return outbound.DialContext(ctx, network, singM.ParseSocksaddr(address))
	}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Timeout: 90 * time.Second}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var complete atomic.Int64
	var failures atomic.Int64
	jobs := make(chan int)
	var wg sync.WaitGroup
	var timingsMu sync.Mutex
	var setupTimes, firstByteTimes, requestTimes []float64
	start := time.Now()
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				requestStart := time.Now()
				var setup, firstByte float64
				n := amount / int64(requests)
				if index == requests-1 {
					n += amount % int64(requests)
				}
				url := "https://" + net.JoinHostPort(c.Landing, strconv.Itoa(dataPort)) + "/" + direction
				var body io.Reader
				method := http.MethodGet
				if direction == "upload" {
					method = http.MethodPost
					body = io.LimitReader(zeroReader{}, n)
				} else {
					url += "?bytes=" + strconv.FormatInt(n, 10)
				}
				req, err := http.NewRequestWithContext(ctx, method, url, body)
				if err != nil {
					failures.Add(1)
					continue
				}
				req.Header.Set("Authorization", "Bearer "+c.HTTPToken)
				trace := &httptrace.ClientTrace{
					GotConn: func(info httptrace.GotConnInfo) {
						if !info.Reused {
							connections.Add(1)
						}
						setup = time.Since(requestStart).Seconds() * 1000
					},
					GotFirstResponseByte: func() { firstByte = time.Since(requestStart).Seconds() * 1000 },
				}
				req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
				if direction == "upload" {
					req.ContentLength = n
				}
				response, err := httpClient.Do(req)
				if err != nil {
					failures.Add(1)
					continue
				}
				ok := response.StatusCode == 200
				if direction == "download" {
					read, e := io.Copy(io.Discard, response.Body)
					ok = ok && e == nil && read == n
				} else {
					var ack struct {
						Received int64 `json:"received"`
					}
					d := json.NewDecoder(io.LimitReader(response.Body, 4096))
					e := d.Decode(&ack)
					var extra any
					ok = ok && e == nil && ack.Received == n && d.Decode(&extra) == io.EOF
				}
				_ = response.Body.Close()
				if ok {
					complete.Add(n)
					timingsMu.Lock()
					setupTimes = append(setupTimes, setup)
					firstByteTimes = append(firstByteTimes, firstByte)
					requestTimes = append(requestTimes, time.Since(requestStart).Seconds()*1000)
					timingsMu.Unlock()
				} else {
					failures.Add(1)
				}
			}
		}()
	}
	for i := 0; i < requests; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			failures.Add(int64(requests - i))
			i = requests
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	emit(map[string]any{"event": "result", "mode": mode, "direction": direction, "concurrency": concurrency, "requests": requests, "complete_bytes": complete.Load(), "failed_requests": failures.Load(), "elapsed_seconds": elapsed, "mbps": float64(complete.Load()) * 8 / elapsed / 1e6, "started_unix": float64(start.UnixNano()) / 1e9, "finished_unix": float64(time.Now().UnixNano()) / 1e9, "connections": connections.Load(), "setup_ms": timingSummary(setupTimes), "first_byte_ms": timingSummary(firstByteTimes), "request_ms": timingSummary(requestTimes)})
	if failures.Load() != 0 || complete.Load() != amount {
		return errors.New("本轮未完整完成，不能作为成功吞吐")
	}
	return nil
}

func timingSummary(values []float64) map[string]float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	percentile := func(p float64) float64 { return values[int(float64(len(values)-1)*p)] }
	return map[string]float64{"min": values[0], "p50": percentile(.5), "p95": percentile(.95), "max": values[len(values)-1]}
}

func emit(v any) { _ = json.NewEncoder(os.Stdout).Encode(v) }
