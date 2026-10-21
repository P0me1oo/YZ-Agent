package agentcli

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/realityrelay"
)

func realityRelayCLIConfig(t *testing.T) string {
	t.Helper()
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YZ_REALITY_RELAY_CLI_TEST_KEY", base64.RawURLEncoding.EncodeToString(private.Bytes()))
	var sid [8]byte
	if _, err := rand.Read(sid[:]); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "relay.yml")
	body := "listen: 127.0.0.1:0\nserver_names: [reality.test]\nkeys:\n  - name: test\n    private_key_env: YZ_REALITY_RELAY_CLI_TEST_KEY\nroutes:\n  - name: route\n    key: test\n    short_id: " + hex.EncodeToString(sid[:]) + "\n    target: 127.0.0.1:443\nhandshake_timeout: 3s\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRealityRelayCommandConfigCheck(t *testing.T) {
	path := realityRelayCLIConfig(t)
	if err := runExperiment([]string{"reality-relay", "--config", path, "--check"}); err != nil {
		t.Fatal("合法实验配置未通过命令行校验")
	}
	for _, args := range [][]string{nil, {"unknown"}, {"reality-relay"}, {"reality-relay", "--config", path, "--stats-interval", "0s"}, {"reality-relay", "--config", path, "extra"}, {"reality-relay", "--unknown"}} {
		if err := runExperiment(args); err == nil {
			t.Fatal("无效实验命令被接受")
		}
	}
}

func TestRealityRelayWatcherRecoversWithoutAnotherFileChange(t *testing.T) {
	path := realityRelayCLIConfig(t)
	cfg, err := realityrelay.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	server, err := realityrelay.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher := relayConfigWatcher{appliedMod: st.ModTime()}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, []byte("copy_mode: buffered\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	changed := st.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	private := os.Getenv("YZ_REALITY_RELAY_CLI_TEST_KEY")
	t.Setenv("YZ_REALITY_RELAY_CLI_TEST_KEY", "")
	now := time.Now()
	if loaded, err := watcher.check(path, server.Reload, now); loaded || err == nil {
		t.Fatal("临时密钥错误没有保留旧配置")
	}
	if server.Stats().CopyMode != "auto" {
		t.Fatal("失败重载改变了生效配置")
	}
	if loaded, err := watcher.check(path, server.Reload, now.Add(100*time.Millisecond)); loaded || err != nil {
		t.Fatal("失败后没有退避")
	}
	// 只修复依赖，不再次改写配置文件或其修改时间。
	t.Setenv("YZ_REALITY_RELAY_CLI_TEST_KEY", private)
	if loaded, err := watcher.check(path, server.Reload, now.Add(2*time.Second)); !loaded || err != nil {
		t.Fatal("依赖恢复后没有重试同一配置版本")
	}
	if server.Stats().CopyMode != "buffered" {
		t.Fatal("恢复后的配置没有生效")
	}
	if loaded, err := watcher.check(path, server.Reload, now.Add(3*time.Second)); loaded || err != nil {
		t.Fatal("成功配置被重复加载")
	}
}

func TestRealityRelayWatcherNewVersionBypassesBackoff(t *testing.T) {
	path := realityRelayCLIConfig(t)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	watcher := relayConfigWatcher{appliedMod: st.ModTime().Add(-time.Second)}
	now := time.Now()
	calls := 0
	reload := func(cfg realityrelay.Config) error {
		calls++
		return os.ErrPermission
	}
	if _, err := watcher.check(path, reload, now); err == nil {
		t.Fatal("首次失败没有报告")
	}
	if _, err := watcher.check(path, reload, now.Add(time.Second)); err != nil || calls != 2 {
		t.Fatal("同一失败没有重试或日志未去重")
	}
	changed := st.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if loaded, err := watcher.check(path, func(cfg realityrelay.Config) error { return nil }, now.Add(1100*time.Millisecond)); !loaded || err != nil {
		t.Fatal("新配置被旧版本的失败退避阻挡")
	}
}
