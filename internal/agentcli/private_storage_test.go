package agentcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

func TestPrivateInstallAndDirectoryMigrationPreserveEncryptedSettings(t *testing.T) {
	dir := t.TempDir()
	token, secret := rand.Text(), rand.Text()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	sum := sha256.Sum256(raw)
	key := privacy.Key{ID: hex.EncodeToString(sum[:16]), Value: base64.StdEncoding.EncodeToString(raw)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var auth map[string]any
		_ = json.NewDecoder(r.Body).Decode(&auth)
		if auth["token"] != token {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "enabled": true, "keys": []privacy.Key{key}}})
	}))
	defer server.Close()
	path := filepath.Join(dir, "config.yml")
	from, to := filepath.ToSlash(filepath.Join(dir, "old")), filepath.ToSlash(filepath.Join(dir, "new"))
	input := fmt.Sprintf("panel:\n  url: %q\nmachine:\n  machine_id: 7\n  token_env: PRIVATE_INSTALL_AUTH\nkernel:\n  type: singbox\n  config_dir: %q\ncert:\n  cert_mode: selfsigned\n  dns_env:\n    PRIVATE_SETTING: %q\nuser_extension:\n  keep: untouched\n", server.URL, from+"/instances/test", secret)
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.env"), []byte("PRIVATE_INSTALL_AUTH="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRIVATE_INSTALL_AUTH", token)
	root, err := config.LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	migrated, err := migratePrivateConfigRoot(path, from, to)
	if err != nil || bytes.Contains(migrated, []byte(secret)) {
		t.Fatalf("迁移泄露私密设置或未完成: %v", err)
	}
	migratedPath := filepath.Join(dir, "migrated.yml")
	if err := os.WriteFile(migratedPath, migrated, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadWritableRoot(migratedPath)
	if err != nil || loaded.Instances[0].Kernel.ConfigDir != to+"/instances/test" {
		t.Fatalf("密文内路径未迁移: %v", err)
	}
	output := filepath.Join(dir, "stage", "config.yml")
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runConfigInit([]string{"--mode", "machine", "--panel-url", server.URL, "--machine-id", "7",
		"--token", token, "--config", migratedPath, "--output", output}); err != nil {
		t.Fatal(err)
	}
	staged, _ := os.ReadFile(output)
	if bytes.Contains(staged, []byte(secret)) || !bytes.Contains(staged, []byte("protected:")) {
		t.Fatal("升级产生明文私密配置")
	}
	updated, err := config.LoadWritableRoot(output)
	if err != nil || updated.Instances[0].Cert.DNSEnv["PRIVATE_SETTING"] != secret {
		t.Fatalf("升级丢失证书设置: %v", err)
	}
	inner, err := privacy.Open([]byte(updated.Instances[0].Protected))
	if err != nil || !bytes.Contains(inner, []byte("user_extension")) {
		t.Fatal("升级丢失配置扩展字段")
	}
}
