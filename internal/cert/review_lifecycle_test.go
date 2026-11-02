package cert

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/caddyserver/certmagic"
)

func TestCertificateReuseChecksDomainExpiryAndMode(t *testing.T) {
	cfg := config.CertConfig{CertMode: "self", Domain: "a.example.test", CertDir: t.TempDir()}
	m := NewManager(cfg)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := m.TLSCert()
	if changed, err := m.Reconfigure(t.Context(), cfg); err != nil || changed {
		t.Fatalf("重复应用改变证书: %v", err)
	}
	cfg.Domain = "b.example.test"
	if changed, err := m.Reconfigure(t.Context(), cfg); err != nil || !changed {
		t.Fatalf("改域名未更新: %v", err)
	}
	if bytes.Equal(first.CertPEM, m.TLSCert().CertPEM) {
		t.Fatal("复用了其他域名证书")
	}
	assertTLSHandshake(t, m, cfg.Domain)
	// 生成公私钥匹配但已过期的保存组，恢复时必须重新生成。
	cert, key := generateSelfSignedPair(t, cfg.Domain)
	block, _ := pem.Decode(cert)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(key)
	private, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotBefore, leaf.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &private.PublicKey, private)
	if err != nil {
		t.Fatal(err)
	}
	cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	m.storePEM(cert, key)
	restored := NewManager(cfg)
	if err := restored.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(cert, restored.TLSCert().CertPEM) {
		t.Fatal("恢复了过期证书")
	}
	assertTLSHandshake(t, restored, cfg.Domain)
	cfg.CertMode = "none"
	if changed, err := restored.Reconfigure(t.Context(), cfg); err != nil || !changed || restored.HasCert() {
		t.Fatalf("关闭证书未清理材料: %v", err)
	}
	for _, name := range []string{"cert-pair.json", "cert.pem", "key.pem"} {
		if _, err := os.Stat(filepath.Join(cfg.CertDir, name)); !os.IsNotExist(err) {
			t.Fatal("关闭模式保留活动材料")
		}
	}
}

func assertTLSHandshake(t *testing.T, manager *Manager, domain string) {
	t.Helper()
	material := manager.TLSCert()
	pair, err := tls.X509KeyPair(material.CertPEM, material.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(material.CertPEM)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	server := tls.Server(a, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	client := tls.Client(b, &tls.Config{RootCAs: roots, ServerName: domain, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.HandshakeContext(ctx) }()
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCertificatePublishingAlwaysRecoversCompletePair(t *testing.T) {
	dir := t.TempDir()
	a, b := NewManager(config.CertConfig{CertMode: "content", CertDir: dir}), NewManager(config.CertConfig{CertMode: "content", CertDir: dir})
	certA, keyA := generateSelfSignedPair(t, "same.example.test")
	certB, keyB := generateSelfSignedPair(t, "same.example.test")
	var wg sync.WaitGroup
	for _, item := range []struct {
		manager   *Manager
		cert, key []byte
	}{{a, certA, keyA}, {b, certB, keyB}} {
		wg.Go(func() {
			for range 30 {
				item.manager.storePEM(item.cert, item.key)
			}
		})
	}
	for range 60 {
		data, err := privacy.ReadFile(filepath.Join(dir, "cert-pair.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var pair persistedPair
		if err := json.Unmarshal(data, &pair); err != nil {
			t.Fatal(err)
		}
		if err := validateCertificate(pair.Cert, pair.Key, "same.example.test"); err != nil {
			t.Fatalf("并发保存暴露不匹配组: %v", err)
		}
	}
	wg.Wait()
}

func TestACMECleanupAndStaleRenewalCannotPublish(t *testing.T) {
	cfg := config.CertConfig{CertMode: "self", Domain: "new.example.test", CertDir: t.TempDir()}
	m := NewManager(cfg)
	cert, key := generateSelfSignedPair(t, "old.example.test")
	for range 5 {
		m.cache = certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return nil, nil }})
		m.acmeStarted = true
		old := m.generation.Add(1)
		if _, err := m.Reconfigure(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if m.cache != nil || m.magic != nil || m.acmeCancel != nil {
			t.Fatal("重新配置遗留缓存")
		}
		var wg sync.WaitGroup
		wg.Go(func() {
			if m.publishRenewal(config.CertConfig{CertDir: cfg.CertDir}, old, cert, key) {
				t.Error("旧续期覆盖新证书")
			}
		})
		wg.Wait()
		assertTLSHandshake(t, m, cfg.Domain)
	}
	m.Stop()
	m.Stop()
	// 被取消的初始化失败也必须停止已经创建的缓存。
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	failed := NewManager(config.CertConfig{CertMode: "http", Domain: "failure.example.invalid", CertDir: t.TempDir()})
	if err := failed.Start(ctx); err == nil {
		t.Fatal("取消初始化意外成功")
	}
	if failed.cache != nil || failed.magic != nil || failed.acmeCancel != nil {
		t.Fatal("初始化失败遗留后台缓存")
	}
}

func TestFileCertificatePersistenceDoesNotRemoveInputFiles(t *testing.T) {
	dir := t.TempDir()
	cert, key := generateSelfSignedPair(t, "file.example.test")
	cfg := config.CertConfig{CertMode: "file", Domain: "file.example.test", CertDir: dir, CertFile: filepath.Join(dir, "cert.pem"), KeyFile: filepath.Join(dir, "key.pem")}
	if err := os.WriteFile(cfg.CertFile, cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.KeyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg)
	for range 2 {
		if _, err := m.Reconfigure(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg.CertMode = "none"
	if _, err := m.Reconfigure(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.CertFile); err != nil {
		t.Fatal("关闭证书删除用户输入文件")
	}
	if m.HasCert() {
		t.Fatal("关闭模式保留活动材料")
	}
}
