package cert

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
	"github.com/caddyserver/certmagic"
)

func TestPrivateCertificatePersistenceAndACMEStorage(t *testing.T) {
	dir := t.TempDir()
	privacytest.Bind(t, dir)
	certPEM, keyPEM := generateSelfSignedPair(t, "private.example.invalid")
	manager := NewManager(config.CertConfig{CertDir: dir})
	manager.storePEM(certPEM, keyPEM)
	raw, err := os.ReadFile(filepath.Join(dir, "key.pem"))
	if err != nil || !privacy.IsEncrypted(raw) || bytes.Contains(raw, keyPEM) {
		t.Fatal("证书私钥仍以明文保存")
	}
	restarted := NewManager(config.CertConfig{CertDir: dir})
	if !restarted.loadPersistedPEM() {
		t.Fatal("重启无法读取加密证书")
	}
	if !bytes.Equal(restarted.TLSCert().KeyPEM, keyPEM) {
		t.Fatal("重启改变证书私钥")
	}
	storage := &privateStorage{Storage: &certmagic.FileStorage{Path: dir}, root: dir}
	if err := storage.Store(context.Background(), "accounts/test.key", keyPEM); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(filepath.Join(dir, "accounts/test.key"))
	if err != nil || !privacy.IsEncrypted(raw) {
		t.Fatal("ACME 私钥未加密")
	}
	loaded, err := storage.Load(context.Background(), "accounts/test.key")
	if err != nil || !bytes.Equal(loaded, keyPEM) {
		t.Fatal("ACME 加密存储不能恢复")
	}
}
