package config

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
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
)

func privacyFixture(t *testing.T) (string, string, *atomic.Bool) {
	t.Helper()
	dir := t.TempDir()
	token, secret := rand.Text(), rand.Text()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	sum := sha256.Sum256(raw)
	key := privacy.Key{ID: hex.EncodeToString(sum[:16]), Value: base64.StdEncoding.EncodeToString(raw)}
	available := &atomic.Bool{}
	available.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			w.WriteHeader(503)
			return
		}
		var auth map[string]any
		_ = json.NewDecoder(r.Body).Decode(&auth)
		if auth["token"] != token {
			w.WriteHeader(403)
			return
		}
		enabled := auth["machine_id"] == float64(1)
		keys := []privacy.Key{}
		if enabled {
			keys = append(keys, key)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1, "enabled": enabled, "keys": keys}})
	}))
	t.Cleanup(server.Close)
	envName := "PRIVACY_FIXTURE_" + rand.Text()
	t.Setenv(envName, token)
	path := filepath.Join(dir, "config.yml")
	text := fmt.Sprintf("panel:\n  url: %q\nmachine:\n  machine_id: 1\n  token_env: %s\nkernel:\n  type: singbox\n  config_dir: %q\ncert:\n  dns_env:\n    PRIVATE_TEST_VALUE: %q\nuser_extension:\n  keep: untouched\n", server.URL, envName, filepath.Join(dir, "instance"), secret)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.env"), []byte(envName+"="+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, secret, available
}

func TestPrivateConfigurationMigrationIsRepeatableAndPreservesPendingData(t *testing.T) {
	path, secret, _ := privacyFixture(t)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := root.Kernel.ConfigDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, "pending-report-test.json")
	data := []byte(`{"pending":{"traffic":{"7":[42,84]}}}`)
	if err := os.WriteFile(pending, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	encoded, _ := os.ReadFile(path)
	if bytes.Contains(encoded, []byte(secret)) || !bytes.Contains(encoded, []byte("protected:")) {
		t.Fatal("敏感配置仍为明文")
	}
	cache, _ := os.ReadFile(pending)
	if !privacy.IsEncrypted(cache) {
		t.Fatal("待确认流量未加密")
	}
	restored, err := privacy.ReadFile(pending)
	if err != nil || !bytes.Equal(data, restored) {
		t.Fatal("迁移改变待确认流量")
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(encoded, again) {
		t.Fatal("重复调用应保持配置稳定")
	}
	loaded, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Instances[0].Cert.DNSEnv["PRIVATE_TEST_VALUE"] != secret {
		t.Fatal("重载丢失敏感设置")
	}
	inner, err := privacy.Open([]byte(loaded.Instances[0].Protected))
	if err != nil || !bytes.Contains(inner, []byte("user_extension")) {
		t.Fatal("加密时丢弃了用户扩展字段")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateColdStartHelper$")
	cmd.Env = append(os.Environ(), "YZ_PRIVATE_CONFIG_TEST="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("新进程无法自动领取密钥恢复配置: %s", out)
	}
}

func TestPrivateManagedCustomConfigNamedConfigYML(t *testing.T) {
	path, _, _ := privacyFixture(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]any
	if err := yaml.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	kernel := original["kernel"].(map[string]any)
	custom := filepath.Join(kernel["config_dir"].(string), "config.yml")
	kernel["custom_config"] = custom
	data, err = yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(custom), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := []byte("outbounds:\n  - tag: custom-exit\n")
	if err := os.WriteFile(custom, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := PreparePrivacy(context.Background(), path, root); err != nil {
			t.Fatal(err)
		}
		stored, err := os.ReadFile(custom)
		if err != nil || !privacy.IsEncrypted(stored) {
			t.Fatalf("managed config.yml must be encrypted: %v", err)
		}
		restored, err := privacy.ReadFile(custom)
		if err != nil || !bytes.Equal(plain, restored) {
			t.Fatalf("managed config.yml could not be restored: %v", err)
		}
		root, err = LoadRoot(path)
		if err != nil || len(root.Instances) != 1 || root.Instances[0].PrivacyError != nil {
			t.Fatalf("private root reload failed: %v", err)
		}
	}
}

func TestPrivateColdStartHelper(t *testing.T) {
	path := os.Getenv("YZ_PRIVATE_CONFIG_TEST")
	if path == "" {
		return
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("YZ_PRIVATE_EXPECT_PARTIAL") == "1" {
		if len(root.Instances) != 2 || root.Instances[0].PrivacyError == nil || root.Instances[1].PrivacyError != nil {
			t.Fatal("一个面板故障影响了其他实例")
		}
		if err := PreparePrivacy(context.Background(), path, root); err != nil || root.Instances[1].PrivacyError != nil {
			t.Fatal("可恢复的实例未能准备启动")
		}
		return
	}
	if len(root.Instances) == 0 || root.Instances[0].PrivacyError != nil {
		t.Fatal("冷启动无法恢复私密配置")
	}
}

func TestPrivatePanelOutageIsIsolatedAndRecovers(t *testing.T) {
	path, _, available := privacyFixture(t)
	other, _, _ := privacyFixture(t)
	first, _ := os.ReadFile(path)
	second, _ := os.ReadFile(other)
	var a, b map[string]any
	_ = yaml.Unmarshal(first, &a)
	_ = yaml.Unmarshal(second, &b)
	combined, _ := yaml.Marshal(map[string]any{"instances": []any{a, b}})
	if err := os.WriteFile(path, combined, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	available.Store(false)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPrivateColdStartHelper$")
	cmd.Env = append(os.Environ(), "YZ_PRIVATE_CONFIG_TEST="+path, "YZ_PRIVATE_EXPECT_PARTIAL=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("故障隔离失败: %s", out)
	}
	available.Store(true)
	cmd = exec.Command(os.Args[0], "-test.run=^TestPrivateColdStartHelper$")
	cmd.Env = append(os.Environ(), "YZ_PRIVATE_CONFIG_TEST="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("面板恢复后无法自动解密: %s", out)
	}
}

func TestPrivateBackupAndLegacyExportPreserveSettingsAndReports(t *testing.T) {
	path, secret, _ := privacyFixture(t)
	before, _ := os.ReadFile(path)
	backup := filepath.Join(filepath.Dir(path), "backups", "owned")
	unknown := filepath.Join(filepath.Dir(path), "backups", "unrelated")
	for _, dir := range []string{backup, unknown} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(backup, "config.yml"), before, 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := []byte("panel:\n  url: https://unrelated.invalid\nmachine:\n  machine_id: 99\n  token_env: UNAVAILABLE_BACKUP_AUTH\n")
	if err := os.WriteFile(filepath.Join(unknown, "config.yml"), unrelated, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.Kernel.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root.Kernel.ConfigDir, "pending-report-export.json")
	payload := []byte(`{"report_id":"export-test","traffic":{"7":[123,456]}}`)
	if err := os.WriteFile(report, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil || root.PrivacyError != nil {
		t.Fatalf("保护失败: %v / %v", err, root.PrivacyError)
	}
	snapshot, _ := os.ReadFile(filepath.Join(backup, "config.yml"))
	if bytes.Contains(snapshot, []byte(secret)) || !hasCipherConfig(snapshot) {
		t.Fatal("可恢复备份未保护")
	}
	unchanged, _ := os.ReadFile(filepath.Join(unknown, "config.yml"))
	if !bytes.Equal(unchanged, unrelated) {
		t.Fatal("不应修改无法确认身份的历史备份")
	}
	legacy, err := ExportLegacyConfig(path)
	if err != nil || hasCipherConfig(legacy) || !bytes.Contains(legacy, []byte(secret)) || !bytes.Contains(legacy, []byte("user_extension")) {
		t.Fatalf("兼容导出丢失设置: %v", err)
	}
	if err := ExportPrivateState(context.Background(), path, backup); err != nil {
		t.Fatal(err)
	}
	recovered, _ := os.ReadFile(report)
	if !bytes.Equal(recovered, payload) {
		t.Fatal("旧版恢复改变了待确认流量")
	}
	snapshot, _ = os.ReadFile(filepath.Join(backup, "config.yml"))
	if hasCipherConfig(snapshot) || !bytes.Contains(snapshot, []byte(secret)) {
		t.Fatal("回滚快照未转换为旧版格式")
	}
}

func TestPrivateConfigurationDoesNotOverwriteConcurrentEdits(t *testing.T) {
	path, _, _ := privacyFixture(t)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	changed := append(before, []byte("\n# later edit\n")...)
	if err := os.WriteFile(path, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err == nil {
		t.Fatal("应拒绝覆盖并发修改")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, changed) {
		t.Fatal("并发修改丢失")
	}
}

func TestProtectedConfigurationUsesCurrentBootstrapCredential(t *testing.T) {
	path, _, _ := privacyFixture(t)
	data, _ := os.ReadFile(path)
	var original map[string]any
	_ = yaml.Unmarshal(data, &original)
	machine := original["machine"].(map[string]any)
	machine["token"] = os.Getenv(machine["token_env"].(string))
	data, _ = yaml.Marshal(original)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	var protected map[string]any
	_ = yaml.Unmarshal(data, &protected)
	entry := protected["instances"].([]any)[0].(map[string]any)
	entry["machine"].(map[string]any)["token_env"] = "ROTATED_PRIVATE_TEST_AUTH"
	rotated := rand.Text()
	t.Setenv("ROTATED_PRIVATE_TEST_AUTH", rotated)
	data, _ = yaml.Marshal(protected)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRoot(path)
	if err != nil || loaded.Instances[0].Machine.Token != rotated {
		t.Fatal("旧密文中的凭据不能覆盖当前启动凭据")
	}
}

func TestPrivateOverlappingMachineDirectoriesCannotEncryptOtherMachinesData(t *testing.T) {
	path, _, _ := privacyFixture(t)
	other, _, _ := privacyFixture(t)
	first, _ := os.ReadFile(path)
	second, _ := os.ReadFile(other)
	var a, b map[string]any
	_ = yaml.Unmarshal(first, &a)
	_ = yaml.Unmarshal(second, &b)
	parent := a["kernel"].(map[string]any)["config_dir"].(string)
	child := filepath.Join(parent, "other-machine")
	b["kernel"].(map[string]any)["config_dir"] = child
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(child, "pending-report-overlap.json")
	data := []byte(`{"report_id":"keep-ownership"}`)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	combined, _ := yaml.Marshal(map[string]any{"instances": []any{a, b}})
	if err := os.WriteFile(path, combined, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(context.Background(), path, root); err != nil {
		t.Fatal(err)
	}
	if root.Instances[0].PrivacyError == nil || root.Instances[1].PrivacyError == nil {
		t.Fatal("重叠数据目录必须停止受影响的实例")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(after, data) {
		t.Fatal("不能用其他机器的密钥迁移已有数据")
	}
}
