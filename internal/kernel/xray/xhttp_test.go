package xray

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/google/uuid"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/core"
	confserial "github.com/xtls/xray-core/infra/conf/serial"
)

func xhttpTestInstance(t *testing.T, cfg M) *core.Instance {
	t.Helper()
	cfg["log"] = M{"loglevel": "none"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := confserial.LoadJSONConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	instance, err := core.New(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err = instance.Start(); err != nil {
		instance.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { instance.Close() })
	return instance
}

func xhttpTestPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func xhttpTestServer(t *testing.T, alpn string) (int, string, M) {
	t.Helper()
	port, id := xhttpTestPort(t), uuid.NewString()
	n := &model.NodeSpec{Protocol: "vless", Network: "xhttp", ServerPort: port, ListenIP: "127.0.0.1", NetworkSettings: M{"path": "/audit"}}
	cert := kernel.TLSCert{}
	security := M{}
	if alpn != "" {
		var pairCert kernel.TLSCert
		pairCert, pair := visionRuntimeCertificate(t)
		cert = pairCert
		pin := sha256.Sum256(pair.Certificate[0])
		n.TLS = 1
		n.TLSSettings = M{"server_name": "localhost", "alpn": []string{alpn}}
		security = M{"security": "tls", "tlsSettings": M{"serverName": "localhost", "alpn": []string{alpn}, "pinnedPeerCertSha256": hex.EncodeToString(pin[:])}}
	}
	cfg := buildConfig(config.KernelConfig{Type: "xray"}, n, []model.UserSpec{{ID: 1, UUID: id}}, cert)
	// 环回目标只用于独立测试实例，覆盖默认的私网屏蔽策略。
	cfg["routing"] = M{"rules": []M{}}
	cfg["outbounds"] = []M{{"protocol": "freedom", "settings": M{"finalRules": []M{{"action": "allow"}}}}}
	xhttpTestInstance(t, cfg)
	return port, id, security
}

func xhttpTestClient(t *testing.T, port int, id string, settings, security M) *core.Instance {
	t.Helper()
	stream := buildTransportStreamSettings("xhttp", settings, "")
	for key, value := range security {
		stream[key] = value
	}
	return xhttpTestInstance(t, M{"outbounds": []M{{"protocol": "vless", "settings": M{"vnext": []M{{"address": "127.0.0.1", "port": port, "users": []M{{"id": id, "encryption": "none"}}}}}, "streamSettings": stream}}})
}

func xhttpTestExchange(t *testing.T, client *core.Instance) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(8 * time.Second))
		io.Copy(c, c)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c, err := core.Dial(ctx, client, xnet.TCPDestination(xnet.LocalHostIP, xnet.Port(listener.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("xhttp-audit-"), 8192)
	done := make(chan error, 1)
	go func() {
		go func() { c.Write(payload) }()
		got := make([]byte, len(payload))
		_, e := io.ReadFull(c, got)
		if e == nil && !bytes.Equal(payload, got) {
			e = io.ErrUnexpectedEOF
		}
		done <- e
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestXHTTPActualModesAndH3(t *testing.T) {
	for _, tc := range []struct{ mode, alpn string }{{"auto", ""}, {"packet-up", ""}, {"stream-up", ""}, {"stream-one", ""}, {"packet-up", "h2"}, {"packet-up", "h3"}, {"stream-one", "h3"}} {
		t.Run(tc.mode+"/"+tc.alpn, func(t *testing.T) {
			p, id, security := xhttpTestServer(t, tc.alpn)
			client := xhttpTestClient(t, p, id, M{"path": "/audit", "mode": tc.mode}, security)
			xhttpTestExchange(t, client)
		})
	}
}

func xhttpTestProxy(t *testing.T, port int, tls bool) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	target, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	tr := &http.Transport{DisableKeepAlives: true}
	proxy.Transport = tr
	t.Cleanup(tr.CloseIdleConnections)
	downloads, uploads := &atomic.Int64{}, &atomic.Int64{}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HTTP/1 上行反代允许边读请求边返回，对应关闭请求缓冲的部署。
		http.NewResponseController(w).EnableFullDuplex()
		if r.Method == "GET" {
			downloads.Add(1)
		} else {
			uploads.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	if tls {
		s.EnableHTTP2 = true
		s.StartTLS()
	} else {
		s.Start()
	}
	t.Cleanup(s.Close)
	return s, downloads, uploads
}

func TestXHTTPActualSplit(t *testing.T) {
	for _, mode := range []string{"auto", "packet-up", "stream-up"} {
		t.Run(mode, func(t *testing.T) {
			p, id, _ := xhttpTestServer(t, "")
			upload, ug, uu := xhttpTestProxy(t, p, false)
			download, dg, du := xhttpTestProxy(t, p, true)
			u, _ := url.Parse(upload.URL)
			d, _ := url.Parse(download.URL)
			up, _ := strconv.Atoi(u.Port())
			down, _ := strconv.Atoi(d.Port())
			pin := sha256.Sum256(download.Certificate().Raw)
			client := xhttpTestClient(t, up, id, M{"path": "/audit", "mode": mode, "downloadSettings": M{"address": "127.0.0.1", "port": down, "network": "xhttp", "security": "tls", "tlsSettings": M{"pinnedPeerCertSha256": hex.EncodeToString(pin[:]), "alpn": []string{"h2"}}, "xhttpSettings": M{"path": "/audit", "headers": []any{}}}}, nil)
			xhttpTestExchange(t, client)
			if ug.Load() != 0 || uu.Load() == 0 || dg.Load() == 0 || du.Load() != 0 {
				t.Fatal("upload and download were not separated")
			}
		})
	}
}
