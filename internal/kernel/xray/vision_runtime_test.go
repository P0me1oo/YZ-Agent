package xray

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	singM "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/testing/realitytest"
)

type visionMeasuredConn struct {
	net.Conn
	raw              net.Conn
	upload, download int64
}

func (c *visionMeasuredConn) Close() error { return c.raw.Close() }

func (c *visionMeasuredConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.download += int64(n)
	return n, err
}
func (c *visionMeasuredConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.upload += int64(n)
	return n, err
}

func visionRuntimeCertificate(t *testing.T) (kernel.TLSCert, tls.Certificate) {
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
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert := kernel.TLSCert{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}),
	}
	pair, err := tls.X509KeyPair(cert.CertPEM, cert.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, pair
}

func visionRuntimeTarget(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				data := make([]byte, 64*1024)
				for {
					_, err := io.ReadFull(conn, data)
					if err != nil {
						return
					}
					if _, err := conn.Write(data); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	return listener.Addr().String()
}

func visionRuntimeNode(t *testing.T) *model.NodeSpec {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return &model.NodeSpec{
		Protocol: "vless", Network: "tcp", ListenIP: "127.0.0.1", ServerPort: port,
		TLS: 1, Flow: vless.FlowVision, TLSSettings: map[string]any{"server_name": "localhost"},
		CustomRoutes: []map[string]any{{"type": "field", "ip": []string{"127.0.0.1/32"}, "outboundTag": "direct"}},
	}
}

func visionRuntimeDial(t *testing.T, node *model.NodeSpec, user model.UserSpec, target string) (*tls.Conn, *visionMeasuredConn) {
	t.Helper()
	raw, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(node.ServerPort)),
		&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, ServerName: "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.NetConn().Close() })
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	client, err := vless.NewClient(user.UUID, vless.FlowVision, logger.NOP())
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := client.DialConn(raw, singM.ParseSocksaddr(target))
	if err != nil {
		t.Fatal(err)
	}
	measured := &visionMeasuredConn{Conn: tunnel, raw: raw.NetConn()}
	inner := tls.Client(measured, &tls.Config{
		InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, ServerName: "localhost",
	})
	if err := inner.Handshake(); err != nil {
		t.Fatal(err)
	}
	return inner, measured
}

func visionRuntimeExchange(t *testing.T, conn *tls.Conn) {
	t.Helper()
	data := bytes.Repeat([]byte("v"), 64*1024)
	if _, err := conn.Write(data); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(data))
	if _, err := io.ReadFull(conn, received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, received) {
		t.Fatal("实际 Vision 传输内容不一致")
	}
}

func visionRuntimeWaitTraffic(t *testing.T, x *Xray, user int, want [2]int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _, _, err := x.GetUserTraffic(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got[user] == want {
			for range 2 {
				again, _, _, err := x.GetUserTraffic(t.Context())
				if err != nil || again[user] != want {
					t.Fatalf("重复采样改变累计: %v %v", again, err)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("实际 Vision 字节计数=%v，期望 %v", got[user], want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 在真实 TLS 1.3 目标与 Vision 客户端之间比较每个加密字节，覆盖已连接重载和重启恢复。
func TestVisionAccountingTLS13Runtime(t *testing.T) {
	for _, tc := range []struct {
		name           string
		limited, relay bool
	}{{"unlimited", false, false}, {"limited", true, false}, {"relay-unlimited", false, true}, {"relay-limited", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cert, pair := visionRuntimeCertificate(t)
			target := visionRuntimeTarget(t, pair)
			_, port, err := net.SplitHostPort(target)
			if err != nil {
				t.Fatal(err)
			}
			node := visionRuntimeNode(t)
			identity, err := uuid.NewV4()
			if err != nil {
				t.Fatal(err)
			}
			identity[6], identity[7] = 0, 0
			user := model.UserSpec{ID: 991, UUID: identity.String()}
			if tc.limited {
				user.SpeedLimit = 64
			}
			cfg := config.KernelConfig{Type: "xray", LogLevel: "fatal", ConfigDir: t.TempDir(),
				CustomOutbound: []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{
					"redirect": target, "finalRules": []map[string]any{{"action": "allow", "network": "tcp",
						"ip": []string{"127.0.0.1/32"}, "port": port}},
				}}},
			}
			clientUser := user
			if tc.relay {
				fixture := realitytest.Start(t, true)
				secret, err := uuid.NewV4()
				if err != nil {
					t.Fatal(err)
				}
				secret[6], secret[7] = 0, 0
				landing := visionRuntimeNode(t)
				landing.TLS = 2
				landing.TLSSettings = map[string]any{"server_name": "reality.test",
					"private_key": base64.RawURLEncoding.EncodeToString(fixture.Config.PrivateKey.Bytes()),
					"short_id":    hex.EncodeToString(fixture.ShortID[:]), "dest": fixture.Config.Dest}
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless",
					VLESS: &model.RelayVLESSConfig{ID: secret.String()}}
				landingCfg := cfg
				landingCfg.ConfigDir = t.TempDir()
				landingCore := New(landingCfg)
				if err := landingCore.Start(landing, nil, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(landingCore.Stop)
				node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 2, Children: []model.RelayChild{{
					NodeID: 11, RouteID: 3, Tag: "relay-line", Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort,
					VLESS: &model.RelayVLESSConfig{ID: secret.String(), Network: "tcp", TLS: 2, Flow: vless.FlowVision, Encryption: "none",
						RealitySettings: map[string]any{"server_name": "reality.test",
							"public_key": base64.RawURLEncoding.EncodeToString(fixture.PublicKey), "short_id": hex.EncodeToString(fixture.ShortID[:])}},
				}}}
				identity[7] = 3
				clientUser.UUID = identity.String()
			}
			x := New(cfg)
			if err := x.Start(node, []model.UserSpec{user}, cert); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(x.Stop)
			// 请求测试地址，由固定 freedom 夹具重定向到本机；不访问外网。
			conn, measured := visionRuntimeDial(t, node, clientUser, "203.0.113.8:443")
			var total [2]int64
			for range 2 {
				visionRuntimeExchange(t, conn)
				want := [2]int64{measured.upload, measured.download}
				visionRuntimeWaitTraffic(t, x, user.ID, want)
				if tc.relay {
					got, err := x.GetRelayUserTraffic(t.Context())
					if err != nil || got[user.ID][11] != want {
						t.Fatalf("实际中转嵌套计数=%v，期望 %v: %v", got, want, err)
					}
				}
				if err := x.Reload(node, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
			}
			total = [2]int64{measured.upload, measured.download}
			// 直接关闭原始 TCP，避免把额外 close_notify 作为未知尾部字节。
			measured.Close()
			x.Stop()
			visionRuntimeWaitTraffic(t, x, user.ID, total)
			if err := x.Start(node, []model.UserSpec{user}, cert); err != nil {
				t.Fatal(err)
			}
			next, nextMeasured := visionRuntimeDial(t, node, clientUser, "203.0.113.8:443")
			visionRuntimeExchange(t, next)
			total[0] += nextMeasured.upload
			total[1] += nextMeasured.download
			visionRuntimeWaitTraffic(t, x, user.ID, total)
			nextMeasured.Close()
		})
	}
}
