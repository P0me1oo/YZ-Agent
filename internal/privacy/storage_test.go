package privacy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testKey(t *testing.T) Key {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return Key{hex.EncodeToString(sum[:16]), base64.StdEncoding.EncodeToString(raw)}
}

func TestEncryptionAuthenticatesCiphertextAndUsesFreshNonces(t *testing.T) {
	key := testKey(t)
	state, err := New([]Key{key})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(rand.Text())
	first, err := state.Seal(secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := state.Seal(secret, nil)
	if bytes.Equal(first, second) || bytes.Contains(first, secret) || bytes.Contains(first, []byte(key.Value)) {
		t.Fatal("密文或随机数保护无效")
	}
	plain, err := Open(first)
	if err != nil || !bytes.Equal(plain, secret) {
		t.Fatal("密文不能恢复")
	}
	var e envelope
	if err := json.Unmarshal(first[len(Magic):], &e); err != nil {
		t.Fatal(err)
	}
	value, _ := base64.StdEncoding.DecodeString(e.Data)
	value[len(value)-1] ^= 1
	e.Data = base64.StdEncoding.EncodeToString(value)
	tampered, _ := json.Marshal(e)
	if _, err := Open(append([]byte(Magic), tampered...)); err == nil {
		t.Fatal("篡改密文必须拒绝")
	}
	registry.Lock()
	delete(registry.keys, key.ID)
	registry.Unlock()
	if _, err := Open(first); err == nil {
		t.Fatal("缺少面板密钥时不能返回明文")
	}
	if _, err := New([]Key{{key.ID, base64.StdEncoding.EncodeToString(make([]byte, 32))}}); err == nil {
		t.Fatal("密钥标识错误必须拒绝")
	}
}

func TestPrivateFileMigrationRotationAndFailureKeepExistingData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	first := testKey(t)
	s, _ := New([]Key{first})
	if err := BindOwner(filepath.Join(dir, "config.yml"), dir, s, nil, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Bind(dir, nil, nil, true) })
	plain := []byte(rand.Text())
	if err := os.WriteFile(path, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateFile(path, s); err != nil {
		t.Fatal(err)
	}
	initial, _ := os.ReadFile(path)
	if !IsEncrypted(initial) {
		t.Fatal("旧文件未加密")
	}
	if err := MigrateFile(path, s); err != nil {
		t.Fatal(err)
	}
	repeated, _ := os.ReadFile(path)
	if !bytes.Equal(initial, repeated) {
		t.Fatal("重复迁移不应改写密文")
	}
	second := testKey(t)
	rotated, _ := New([]Key{second, first})
	if err := BindOwner(filepath.Join(dir, "config.yml"), dir, rotated, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := MigrateFile(path, rotated); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("密钥轮换丢失原内容")
	}
	before, _ := os.ReadFile(path)
	if err := WriteFile(dir, []byte("invalid"), 0o600); err == nil {
		t.Fatal("目录不能作为文件覆盖")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("失败写入改变原文件")
	}
	credential := filepath.Join(dir, "credentials.env")
	if err := WriteFile(credential, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	visible, _ := os.ReadFile(credential)
	if !bytes.Equal(visible, plain) {
		t.Fatal("启动凭据不能变成服务管理器无法读取的格式")
	}
}

func TestProtectionExemptsOnlyRegisteredBootstrapPaths(t *testing.T) {
	for _, ownerName := range []string{"config.yml", "custom-startup.yaml", ""} {
		t.Run("owner="+ownerName, func(t *testing.T) {
			dir := t.TempDir()
			state, err := New([]Key{testKey(t)})
			if err != nil {
				t.Fatal(err)
			}
			owner := ""
			if ownerName != "" {
				owner = filepath.Join(dir, ownerName)
			}
			if err := BindOwner(owner, dir, state, nil, true); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { Bind(dir, nil, nil, true) })
			paths := []string{"config.yml", "credentials.env", "install-meta.json",
				"kernel/config.yml", "kernel/credentials.env", "kernel/install-meta.json"}
			if ownerName != "" && ownerName != "config.yml" {
				paths = append(paths, ownerName)
			}
			plain := []byte(rand.Text())
			for _, name := range paths {
				path := filepath.Join(dir, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, plain, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := MigrateFile(path, state); err != nil {
					t.Fatal(err)
				}
				stored, err := os.ReadFile(path)
				exempt := owner != "" && (path == owner || name == "credentials.env" || name == "install-meta.json")
				if err != nil || IsEncrypted(stored) == exempt {
					t.Fatalf("incorrect protection for %s (exempt=%v): %v", name, exempt, err)
				}
				if err := MigrateFile(path, state); err != nil {
					t.Fatal(err)
				}
				again, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(stored, again) {
					t.Fatalf("repeated migration changed %s: %v", name, err)
				}
				if err := WriteFile(path, plain, 0o600); err != nil {
					t.Fatal(err)
				}
				written, err := os.ReadFile(path)
				if err != nil || IsEncrypted(written) == exempt {
					t.Fatalf("incorrect protection when rewriting %s: %v", name, err)
				}
				restored, err := ReadFile(path)
				if err != nil || !bytes.Equal(plain, restored) {
					t.Fatalf("contents changed for %s: %v", name, err)
				}
			}
		})
	}
}

func TestConfigLockHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	unlock, err := ConfigLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if release, err := ConfigLock(ctx, path); err == nil {
		release()
		t.Fatal("重复锁应等待或超时")
	}
}
