package config

import (
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
)

func TestHTTPConfirmationSurvivesEncryptedConfigAndLocalRemoval(t *testing.T) {
	var policy atomic.Bool
	policy.Store(true)
	path, _, _ := privacyFixtureWithPolicy(t, &policy)
	root, err := LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	root.Panel.HTTPConfirmedURL = root.Panel.URL
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["panel"].(map[string]any)["http_confirmed_url"] = root.Panel.URL
	raw, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	root, err = LoadRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivacy(t.Context(), path, root); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "http_confirmed_url:") || !strings.Contains(string(raw), "protected:") {
		t.Fatal("文件加密后丢失公开的 HTTP 确认信息")
	}
	privacy.ResetOwner(path)
	reloaded, err := LoadWritableRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	item := privacyItems(reloaded)[0]
	if bootstrapFor(item, path).HTTPConfirmedURL != root.Panel.URL {
		t.Fatal("重启后无法恢复确认")
	}
	document = nil
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	entry := document["instances"].([]any)[0].(map[string]any)
	delete(entry["panel"].(map[string]any), "http_confirmed_url")
	raw, err = yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// 回环地址允许此测试继续解密；旧密文中的确认不能恢复已删除的本地授权。
	reloaded, err = LoadWritableRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if privacyItems(reloaded)[0].Panel.HTTPConfirmedURL != "" {
		t.Fatal("旧密文恢复了已撤回的 HTTP 确认")
	}
	policy.Store(false)
	if err := PreparePrivacy(t.Context(), path, reloaded); err != nil {
		t.Fatal(err)
	}
	restored, err := LoadWritableRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if privacyItems(restored)[0].Panel.HTTPConfirmedURL != "" {
		t.Fatal("关闭文件加密后恢复了已撤回的 HTTP 确认")
	}
}
