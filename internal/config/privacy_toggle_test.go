package config

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
)

func TestPrivacyPanelTogglePreservesFilesBackupsAndExtensions(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, secret, _ := privacyFixtureWithPolicy(t, &policy)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := root.Kernel.ConfigDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, "pending-toggle.json")
	payload := []byte(`{"report_id":"unchanged","traffic":{"7":[123,456]}}`)
	if err := os.WriteFile(pending, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(filepath.Dir(path), "backups", "owned", "config.yml")
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(backup, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil || root.PrivacyError != nil {
		t.Fatalf("initial protection failed: %v / %v", err, root.PrivacyError)
	}
	initial := root.Config
	// 生成配置及缓存的文件名并不都符合历史迁移前缀，关闭时也必须还原。
	generated := filepath.Join(dir, "runtime-config.json")
	if err := privacy.WriteFile(generated, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, false, true, true, false} {
		policy.Store(enabled)
		root, err = LoadRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := PreparePrivacy(context.Background(), path, root); err != nil {
			t.Fatal(err)
		}
		for _, c := range privacyItems(root) {
			if c.PrivacyError != nil || c.Cert.DNSEnv["PRIVATE_TEST_VALUE"] != secret {
				t.Fatalf("toggle lost settings or failed: %v", c.PrivacyError)
			}
		}
		for _, name := range []string{path, backup} {
			raw, err := os.ReadFile(name)
			if err != nil || hasCipherConfig(raw) != enabled || bytes.Contains(raw, []byte(secret)) == enabled {
				t.Fatalf("configuration toggle failed for %s: %v", name, err)
			}
			if !enabled && !bytes.Contains(raw, []byte("user_extension")) {
				t.Fatal("closing privacy dropped extension fields")
			}
		}
		raw, err := os.ReadFile(pending)
		if err != nil || privacy.IsEncrypted(raw) != enabled {
			t.Fatalf("traffic storage mode did not change: %v", err)
		}
		plain, err := privacy.ReadFile(pending)
		if err != nil || !bytes.Equal(plain, payload) {
			t.Fatal("toggle changed report identifiers or traffic")
		}
		if !enabled {
			// 尚未退出的旧运行实例不能重新加密文件。
			BindPrivacy(&initial)
			if err := privacy.WriteFile(pending, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{pending, generated} {
				raw, _ := os.ReadFile(name)
				if !bytes.Equal(raw, payload) {
					t.Fatal("old instance reencrypted restored files")
				}
			}
		}
	}
}

func TestPrivacyDisableBeforeColdStartCanRecoverWithoutCachedKeys(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, secret, _ := privacyFixtureWithPolicy(t, &policy)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	policy.Store(false)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivacyToggleColdStartHelper$")
	cmd.Env = append(os.Environ(), "YZ_PRIVACY_TOGGLE_CONFIG="+path, "YZ_PRIVACY_TOGGLE_SECRET="+secret)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("closing privacy before reboot prevented recovery: %v\n%s", err, out)
	}
}

func TestPrivacyToggleColdStartHelper(t *testing.T) {
	path := os.Getenv("YZ_PRIVACY_TOGGLE_CONFIG")
	if path == "" {
		return
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	for _, c := range privacyItems(root) {
		if c.PrivacyError != nil || c.Protected != "" || c.Cert.DNSEnv["PRIVATE_TEST_VALUE"] != os.Getenv("YZ_PRIVACY_TOGGLE_SECRET") {
			t.Fatalf("cold recovery did not finish: %v", c.PrivacyError)
		}
	}
	raw, _ := os.ReadFile(path)
	if hasCipherConfig(raw) {
		t.Fatal("cold recovery left configuration encrypted")
	}
}

func TestPrivacyDisableFailedDecryptionPreservesConfigAndRetries(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, _, _ := privacyFixtureWithPolicy(t, &policy)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := root.Kernel.ConfigDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, "pending-retry.json")
	payload := []byte(`{"report_id":"retry","traffic":{"1":[2,3]}}`)
	if err := os.WriteFile(pending, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := os.ReadFile(pending)
	broken := append([]byte(nil), snapshot...)
	broken[len(broken)-2] ^= 1
	if err := os.WriteFile(pending, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	policy.Store(false)
	root, err = LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	if root.Instances[0].PrivacyError == nil {
		t.Fatal("corrupt encrypted file must prevent completion")
	}
	raw, _ := os.ReadFile(path)
	if !hasCipherConfig(raw) {
		t.Fatal("failed restoration removed startup recovery information")
	}
	unchanged, _ := os.ReadFile(pending)
	if !bytes.Equal(unchanged, broken) {
		t.Fatal("failed restoration overwrote corrupt file")
	}
	if err := os.WriteFile(pending, snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err = LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil || root.Instances[0].PrivacyError != nil {
		t.Fatalf("restoration did not retry: %v", err)
	}
	plain, _ := os.ReadFile(pending)
	if !bytes.Equal(plain, payload) {
		t.Fatal("retry changed traffic")
	}
}

func TestPrivacyToggleIsIsolatedBetweenInstances(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, firstSecret, _ := privacyFixtureWithPolicy(t, &policy)
	other, secondSecret, _ := privacyFixture(t)
	entries := []any{}
	for _, name := range []string{path, other} {
		raw, _ := os.ReadFile(name)
		var entry map[string]any
		if err := yaml.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	raw, _ := yaml.Marshal(map[string]any{"instances": entries})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false, true} {
		policy.Store(enabled)
		root, err := LoadRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := PreparePrivacy(context.Background(), path, root); err != nil {
			t.Fatal(err)
		}
		for _, c := range privacyItems(root) {
			if c.PrivacyError != nil {
				t.Fatal(c.PrivacyError)
			}
		}
		raw, _ = os.ReadFile(path)
		if bytes.Contains(raw, []byte(firstSecret)) == enabled || bytes.Contains(raw, []byte(secondSecret)) {
			t.Fatal("one instance toggle changed the other instance privacy")
		}
	}
}

func TestPrivacyDisableDoesNotBorrowAnotherMachineCachedKey(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, _, _ := privacyFixtureWithPolicy(t, &policy)
	other, _, _ := privacyFixture(t)
	for _, name := range []string{path, other} {
		root, err := LoadRoot(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := PreparePrivacy(context.Background(), name, root); err != nil {
			t.Fatal(err)
		}
	}
	var first, second map[string]any
	raw, _ := os.ReadFile(path)
	if err := yaml.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(other)
	if err := yaml.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	first["instances"].([]any)[0].(map[string]any)["protected"] = second["instances"].([]any)[0].(map[string]any)["protected"]
	tampered, _ := yaml.Marshal(first)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	policy.Store(false)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err == nil {
		t.Fatal("closing privacy used another machine's cached config key")
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, tampered) {
		t.Fatal("foreign encrypted configuration was overwritten")
	}
}
