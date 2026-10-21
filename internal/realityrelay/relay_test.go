package realityrelay

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(t testing.TB) (Config, *ecdh.PrivateKey, [8]byte) {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YZ_REALITY_RELAY_TEST_KEY", base64.RawURLEncoding.EncodeToString(private.Bytes()))
	var sid [8]byte
	if _, err := rand.Read(sid[:]); err != nil {
		t.Fatal(err)
	}
	return Config{Listen: "127.0.0.1:0", ServerNames: []string{"reality.test"}, Keys: []KeyConfig{{Name: "test", PrivateKeyEnv: "YZ_REALITY_RELAY_TEST_KEY"}}, Routes: []RouteConfig{{Name: "first", Key: "test", ShortID: hex.EncodeToString(sid[:]), Target: "127.0.0.1:1"}}}, private, sid
}

func vector(b []byte, n int) []byte {
	out := make([]byte, n, len(b)+n)
	for i := n - 1; i >= 0; i-- {
		out[i] = byte(len(b) >> (8 * (n - 1 - i)))
	}
	return append(out, b...)
}

func extension(id uint16, value []byte) []byte {
	return append([]byte{byte(id >> 8), byte(id)}, vector(value, 2)...)
}

// 独立生成客户端的认证握手；长期密钥、临时密钥及 shortId 均在测试时生成。
func testHello(t testing.TB, private *ecdh.PrivateKey, sid [8]byte, hybrid bool, at time.Time) []byte {
	t.Helper()
	peer, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte{3, 3}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	body = append(body, random...)
	body = append(body, vector(make([]byte, 32), 1)...)
	body = append(body, 0, 2, 0x13, 1, 1, 0)
	sni := append([]byte{0}, vector([]byte("reality.test"), 2)...)
	ext := extension(0, vector(sni, 2))
	ext = append(ext, extension(43, []byte{2, 3, 4})...)
	group := uint16(29)
	public := peer.PublicKey().Bytes()
	if hybrid {
		group = 4588
		public = append(make([]byte, 1184), public...)
	}
	share := append([]byte{byte(group >> 8), byte(group)}, vector(public, 2)...)
	ext = append(ext, extension(51, vector(share, 2))...)
	body = append(body, vector(ext, 2)...)
	raw := append([]byte{1, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	shared, err := peer.ECDH(private.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	auth, err := hkdf.Key(sha256.New, shared, random[:20], "REALITY", 32)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(auth)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 16)
	plain[0] = 26
	binary.BigEndian.PutUint32(plain[4:8], uint32(at.Unix()))
	copy(plain[8:], sid[:])
	ciphertext := aead.Seal(nil, random[20:], plain, raw)
	copy(raw[39:71], ciphertext)
	return raw
}

func tlsRecords(raw []byte, fragment int) []byte {
	var wire []byte
	for len(raw) > 0 {
		n := min(fragment, len(raw))
		wire = append(wire, 22, 3, 1, byte(n>>8), byte(n))
		wire = append(wire, raw[:n]...)
		raw = raw[n:]
	}
	return wire
}

func TestHelloAuthenticationAndFragmentation(t *testing.T) {
	cfg, private, sid := testConfig(t)
	c, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, hybrid := range []bool{false, true} {
		for _, fragment := range []int{1, 7, 97, 16384} {
			raw := testHello(t, private, sid, hybrid, time.Now())
			wire := tlsRecords(raw, fragment)
			prefix, hello, err := readHello(bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(prefix, wire) {
				t.Fatal("原始握手被改写")
			}
			before := append([]byte(nil), hello.raw...)
			r, err := c.identify(hello, time.Now())
			if err != nil || r.name != "first" {
				t.Fatal("合法线路未通过认证")
			}
			if !bytes.Equal(before, hello.raw) {
				t.Fatal("认证修改了原始 ClientHello")
			}
		}
	}
}

func TestRejectWrongAuthenticationAndMalformedHello(t *testing.T) {
	cfg, private, sid := testConfig(t)
	c, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	unknown := sid
	unknown[0] ^= 0xff
	for _, tc := range []struct {
		name string
		key  *ecdh.PrivateKey
		sid  [8]byte
		at   time.Time
	}{
		{"wrong-key", other, sid, time.Now()}, {"unknown-route", private, unknown, time.Now()}, {"expired", private, sid, time.Now().Add(-time.Hour)}, {"future", private, sid, time.Now().Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := parseHello(testHello(t, tc.key, tc.sid, false, tc.at))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.identify(h, time.Now()); err == nil {
				t.Fatal("错误认证被放行")
			}
		})
	}
	raw := testHello(t, private, sid, false, time.Now())
	for i := 0; i < len(raw); i++ {
		if _, _, err := readHello(bytes.NewReader(tlsRecords(raw[:i], 16384))); err == nil {
			t.Fatal("截断握手被放行")
		}
	}
	tampered := append([]byte(nil), raw...)
	tampered[40] ^= 1
	h, err := parseHello(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.identify(h, time.Now()); err == nil {
		t.Fatal("损坏认证被放行")
	}
	for _, bad := range [][]byte{{22, 3, 1, 0, 0}, {23, 3, 3, 0, 1, 1}, {22, 3, 1, 255, 255}} {
		if _, _, err := readHello(bytes.NewReader(bad)); err == nil {
			t.Fatal("错误 TLS 记录被放行")
		}
	}
}

func TestConfigValidationAndRedaction(t *testing.T) {
	cfg, _, _ := testConfig(t)
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ServerNames = nil }, func(c *Config) { c.CopyMode = "unknown" }, func(c *Config) { c.Routes = append(c.Routes, c.Routes[0]) },
		func(c *Config) { c.Keys = append(c.Keys, c.Keys[0]) }, func(c *Config) { c.Routes[0].Target = "localhost:0" }, func(c *Config) { c.Keys[0].PrivateKeyFile = "not-present" },
		func(c *Config) { c.MaxConnections = 1; c.MaxHandshakes = 2 }, func(c *Config) { c.Routes[0].ShortID = "odd" },
	} {
		copy := cfg
		copy.Keys = append([]KeyConfig(nil), cfg.Keys...)
		copy.Routes = append([]RouteConfig(nil), cfg.Routes...)
		mutate(&copy)
		if _, err := New(copy); err == nil {
			t.Fatal("无效配置被接受")
		}
	}
	secret := "never-print-this-invalid-private-key"
	t.Setenv("YZ_REALITY_RELAY_TEST_KEY", secret)
	if _, err := New(cfg); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("私钥校验失败或错误信息泄露输入")
	}
	path := filepath.Join(t.TempDir(), "relay.yml")
	if err := os.WriteFile(path, []byte("unknown_private_key: "+secret), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("YAML 校验错误泄露输入")
	}
}

func TestMultipleKeysPrivateFileAndRotation(t *testing.T) {
	cfg, first, sid := testConfig(t)
	second, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(second.Bytes())), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Keys = append(cfg.Keys, KeyConfig{Name: "second-key", PrivateKeyFile: path})
	cfg.Routes = append(cfg.Routes, RouteConfig{Name: "second", Key: "second-key", ShortID: hex.EncodeToString(sid[:]), Target: "127.0.0.1:2"})
	c, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range []*ecdh.PrivateKey{first, second} {
		h, err := parseHello(testHello(t, key, sid, true, time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		r, err := c.identify(h, time.Now())
		if err != nil || r != c.routes[i] {
			t.Fatal("多组密钥没有正确选择线路")
		}
	}
	rotated, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YZ_REALITY_RELAY_TEST_KEY", base64.RawURLEncoding.EncodeToString(rotated.Bytes()))
	next, err := compileConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.fingerprint == c.fingerprint {
		t.Fatal("密钥轮换没有改变配置快照")
	}
	oldHello, err := parseHello(testHello(t, first, sid, false, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.identify(oldHello, time.Now()); err == nil {
		t.Fatal("轮换后仍接受旧密钥")
	}
	newHello, err := parseHello(testHello(t, rotated, sid, false, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.identify(newHello, time.Now()); err != nil {
		t.Fatal("轮换后没有接受新密钥")
	}
}

func TestPendingHandshakeCapacityRecovers(t *testing.T) {
	cfg, _, _ := testConfig(t)
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.MaxConnections = 2
	cfg.MaxHandshakes = 1
	s, address := startTestServer(t, cfg)
	first, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	waitFor(t, func() bool { return s.Stats().Active == 1 })
	second, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("超出握手容量的连接未关闭")
	}
	if s.Stats().Rejected != 1 {
		t.Fatal("握手容量拒绝未被统计")
	}
	_ = first.Close()
	waitFor(t, func() bool { return s.Stats().Active == 0 })
	third, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	waitFor(t, func() bool { return s.Stats().Active == 1 })
}

func startTestServer(t testing.TB, cfg Config) (*Server, string) {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), l) }()
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("实验入口未及时停止")
		}
	})
	return s, l.Addr().String()
}

func markerBackend(t testing.TB, marker byte, prefix []byte) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				b, err := io.ReadAll(c)
				if err != nil {
					return
				}
				if !bytes.HasPrefix(b, prefix) {
					t.Error("落地收到的握手与原始握手不同")
					return
				}
				_, _ = c.Write(append([]byte{marker}, b[len(prefix):]...))
			}()
		}
	}()
	t.Cleanup(func() { _ = l.Close(); <-done; wg.Wait() })
	return l.Addr().String()
}

func transfer(t testing.TB, address string, prefix, payload []byte) []byte {
	t.Helper()
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	for _, b := range [][]byte{prefix, payload} {
		if _, err := writeAll(c, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestServerSelectsRouteAndPreservesBytesAndHalfClose(t *testing.T) {
	for _, mode := range []string{"auto", "buffered"} {
		t.Run(mode, func(t *testing.T) {
			cfg, private, sid := testConfig(t)
			cfg.CopyMode = mode
			prefix := tlsRecords(testHello(t, private, sid, true, time.Now()), 97)
			cfg.Routes[0].Target = markerBackend(t, 'A', prefix)
			s, address := startTestServer(t, cfg)
			payload := bytes.Repeat([]byte("relay-payload"), 32768)
			got := transfer(t, address, prefix, payload)
			if len(got) != len(payload)+1 || got[0] != 'A' || !bytes.Equal(got[1:], payload) {
				t.Fatal("完整传输或 TCP 半关闭失败")
			}
			waitFor(t, func() bool { return s.Stats().Active == 0 })
			stats := s.Stats().Routes[0]
			if stats.Upload != uint64(len(prefix)+len(payload)) || stats.Download != uint64(len(got)) {
				t.Fatal("线路字节统计不符")
			}
		})
	}
}

func TestReloadFailureRepeatAndRouteRecovery(t *testing.T) {
	cfg, private, sid := testConfig(t)
	prefix := tlsRecords(testHello(t, private, sid, false, time.Now()), 16384)
	cfg.Routes[0].Target = markerBackend(t, 'A', prefix)
	s, address := startTestServer(t, cfg)
	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	bad := cfg
	bad.ServerNames = nil
	if err := s.Reload(bad); err == nil {
		t.Fatal("错误重载未被拒绝")
	}
	if got := transfer(t, address, prefix, []byte("first")); !bytes.Equal(got, []byte("Afirst")) {
		t.Fatal("错误重载影响了旧线路")
	}
	waitFor(t, func() bool { return s.Stats().Active == 0 })
	next := cfg
	next.Routes = append([]RouteConfig(nil), cfg.Routes...)
	next.Routes[0].Target = markerBackend(t, 'B', prefix)
	if err := s.Reload(next); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(next); err != nil {
		t.Fatal(err)
	}
	if got := transfer(t, address, prefix, []byte("next")); !bytes.Equal(got, []byte("Bnext")) {
		t.Fatal("新线路未生效")
	}
	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := transfer(t, address, prefix, []byte("back")); !bytes.Equal(got, []byte("Aback")) {
		t.Fatal("线路恢复失败")
	}
}

func TestRejectTimeoutUnknownRouteAndStop(t *testing.T) {
	cfg, private, sid := testConfig(t)
	cfg.HandshakeTimeout = 30 * time.Millisecond
	cfg.MaxHandshakes = 1
	s, address := startTestServer(t, cfg)
	c, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("空握手没有超时关闭")
	}
	unknown := sid
	unknown[1] ^= 0xff
	if got := transfer(t, address, tlsRecords(testHello(t, private, unknown, false, time.Now()), 16384), nil); len(got) != 0 {
		t.Fatal("未知线路被转发")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(cfg); err == nil {
		t.Fatal("停止后仍接受重载")
	}
}

func TestReloadKeepsIdenticalLiveConnectionAndRevokesChangedOne(t *testing.T) {
	cfg, private, sid := testConfig(t)
	prefix := tlsRecords(testHello(t, private, sid, false, time.Now()), 16384)
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		c, err := backend.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, len(prefix))
		if _, err := io.ReadFull(c, got); err != nil {
			return
		}
		if !bytes.Equal(got, prefix) {
			t.Error("持久连接首包被改写")
			return
		}
		_, _ = io.Copy(c, c)
	}()
	cfg.Routes[0].Target = backend.Addr().String()
	s, address := startTestServer(t, cfg)
	c, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := writeAll(c, prefix); err != nil {
		t.Fatal(err)
	}
	exchange := func() {
		t.Helper()
		if _, err := c.Write([]byte("echo")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil || string(b) != "echo" {
			t.Fatal("存量连接被意外中断")
		}
	}
	exchange()
	for range 2 {
		if err := s.Reload(cfg); err != nil {
			t.Fatal(err)
		}
		exchange()
	}
	bad := cfg
	bad.CopyMode = "invalid"
	if err := s.Reload(bad); err == nil {
		t.Fatal("错误配置未被拒绝")
	}
	exchange()
	next := cfg
	next.Routes = append([]RouteConfig(nil), cfg.Routes...)
	next.Routes[0].Target = "127.0.0.1:1"
	if err := s.Reload(next); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("旧线路在变更后没有撤销")
	}
	select {
	case <-backendDone:
	case <-time.After(3 * time.Second):
		t.Fatal("旧落地连接没有清理")
	}
}

func waitFor(t testing.TB, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待状态收敛超时")
}

func FuzzReadHello(f *testing.F) {
	f.Add([]byte{22, 3, 1, 0, 4, 1, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > maxFlightSize {
			return
		}
		_, _, _ = readHello(bytes.NewReader(b))
	})
}

func BenchmarkAuthenticate(b *testing.B) {
	cfg, private, sid := testConfig(b)
	c, err := compileConfig(cfg)
	if err != nil {
		b.Fatal(err)
	}
	h, err := parseHello(testHello(b, private, sid, true, time.Now()))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.identify(h, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
