//go:build with_quic

package xray

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/quic-go"
	boxTLS "github.com/sagernet/sing-box/common/tls"
)

// 使用每次运行生成的密钥和证书，检查 Xray HY2 的真实 QUIC 握手状态。
func TestHysteria2ECHHandshake(t *testing.T) {
	configPEM, keyPEM, err := boxTLS.ECHKeygenDefault("public.ech.example")
	if err != nil {
		t.Fatal(err)
	}
	configBlock, _ := pem.Decode([]byte(configPEM))
	keyBlock, _ := pem.Decode([]byte(keyPEM))
	for _, source := range []string{"pem", "base64", "file"} {
		t.Run(source, func(t *testing.T) {
			ech := map[string]any{"enabled": true, "key": keyPEM}
			switch source {
			case "base64":
				ech["key"] = base64.StdEncoding.EncodeToString(keyBlock.Bytes)
			case "file":
				path := filepath.Join(t.TempDir(), "ech.pem")
				if err := os.WriteFile(path, []byte(keyPEM), 0600); err != nil {
					t.Fatal(err)
				}
				delete(ech, "key")
				ech["key_path"] = path
			}
			node := &model.NodeSpec{
				Protocol: "hysteria", Version: 2, ListenIP: "127.0.0.1", ServerPort: hy2TestPort(t, "udp"),
				TLSSettings: map[string]any{"ech": ech},
			}
			cert := hy2TestCertificate(t)
			user := model.UserSpec{ID: 701, UUID: hy2TestUUID(t).String()}
			x := New(config.KernelConfig{Type: "xray", LogLevel: "fatal", ConfigDir: t.TempDir()})
			t.Cleanup(x.Stop)
			if err := x.Start(node, []model.UserSpec{user}, cert); err != nil {
				t.Fatalf("HY2 ECH 启动失败: %v", err)
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(cert.CertPEM) {
				t.Fatal("无法加载测试证书")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			conn, err := quic.DialAddr(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(node.ServerPort)), &tls.Config{
				ServerName: "localhost", RootCAs: roots, NextProtos: []string{"h3"},
				EncryptedClientHelloConfigList: configBlock.Bytes,
			}, nil)
			if err != nil {
				t.Fatalf("HY2 ECH 握手失败: %v", err)
			}
			defer conn.CloseWithError(0, "")
			if !conn.ConnectionState().TLS.ECHAccepted {
				t.Fatal("HY2 握手未接受 ECH")
			}
		})
	}
}

func TestHysteria2ECHInvalidKeys(t *testing.T) {
	configPEM, _, err := boxTLS.ECHKeygenDefault("public.ech.example")
	if err != nil {
		t.Fatal(err)
	}
	cert := hy2TestCertificate(t)
	for _, tc := range []struct {
		name string
		ech  map[string]any
	}{
		{"missing", map[string]any{}},
		{"unreadable", map[string]any{"key_path": filepath.Join(t.TempDir(), "absent.pem")}},
		{"public-config", map[string]any{"key": configPEM}},
		{"invalid-base64", map[string]any{"key": "invalid key encoding"}},
		{"invalid-list", map[string]any{"key": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})}},
		{"empty-entry", map[string]any{"key": base64.StdEncoding.EncodeToString([]byte{0, 0, 0, 0})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.ech["enabled"] = true
			node := &model.NodeSpec{Protocol: "hysteria", Version: 2, TLSSettings: map[string]any{"ech": tc.ech}}
			if _, err := marshalConfig(config.KernelConfig{}, node, nil, cert); err == nil || !strings.Contains(err.Error(), "ECH") {
				t.Fatalf("无效密钥必须阻止配置生成: %v", err)
			}
		})
	}
}

func TestHysteria2ECHValidatesGeneratedKeys(t *testing.T) {
	_, keyPEM, err := boxTLS.ECHKeygenDefault("public.ech.example")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ech.pem")
	if err := os.WriteFile(path, []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}
	node := &model.NodeSpec{Protocol: "hysteria", Version: 2,
		TLSSettings: map[string]any{"ech": map[string]any{"enabled": true, "key_path": path}}}
	cfg := buildConfig(config.KernelConfig{}, node, nil, hy2TestCertificate(t))
	// 文件在配置生成后发生变化时，必须校验和使用已经读取的同一份密钥。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := validateHysteriaECHKeys(node, cfg); err != nil {
		t.Fatalf("已生成的有效 ECH 配置不应重新读取文件: %v", err)
	}
}
