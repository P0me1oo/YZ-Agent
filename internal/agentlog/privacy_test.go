package agentlog

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestPrivateCollectorEncryptsQueueAndRetriesOriginalBatch(t *testing.T) {
	const message = `private cached runtime token=fixture-value https://example.invalid/api uuid=00000000-0000-0000-0000-000000000000 {"config":"original"}`
	received := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/server/handshake" {
			w.Write([]byte(`{"log_policy":{"version":1,"disabled":true},"agent_log":{"version":1}}`))
			return
		}
		var batch panel.AgentLogBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		if len(batch.Records) != 1 || batch.Records[0].Message != message {
			t.Error("original diagnostics lost during encrypted cache recovery or upload")
		}
		received = append(received, batch.ID)
		if len(received) == 1 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"batch_id": batch.ID}})
	}))
	defer server.Close()
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	// 模拟日志策略先到、文件迁移尚未执行的窗口，缓存仍必须立即加密。
	privacy.Bind(dir, nil, nil, true)
	scope, policy := t.Name(), nlog.LogPolicyKey(server.URL, 2, 0)
	nlog.ConfigureLogPolicies([]string{policy})
	t.Cleanup(func() { nlog.ConfigureLogPolicies(nil) })
	nlog.RegisterLogScope(scope, policy, filepath.Join(dir, "runtime.log.enc"), state)
	client := panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 2})
	if _, err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !client.AgentLogEnabled() {
		t.Fatal("privacy disabled reporting capability")
	}
	path := filepath.Join(dir, "pending-agent.json")
	c := &collector{path: path, scope: scope, key: state}
	stop := nlog.Subscribe(scope, c.add)
	defer stop()
	nlog.Publish(scope, nlog.Event{Source: "agent", Level: "error", Message: message, OccurredAt: time.Now().Unix()})
	c.flush(t.Context(), client)
	raw, err := os.ReadFile(path)
	if err != nil || !privacy.IsEncrypted(raw) || bytes.Contains(raw, []byte("private cached runtime")) {
		t.Fatal("queue snapshot was not encrypted", err)
	}
	if len(received) != 1 || c.pending == nil {
		t.Fatal("failed report lost fixed batch")
	}
	restarted := &collector{path: path, scope: scope, key: state}
	if err := restarted.restore(); err != nil {
		t.Fatal(err)
	}
	restarted.flush(t.Context(), client)
	if len(received) != 2 || received[0] != received[1] || restarted.pending != nil {
		t.Fatal("restart changed batch or prevented private upload")
	}
	foreign := privacytest.Bind(t, t.TempDir())
	foreignData, err := foreign.Seal([]byte("other machine queue"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, foreignData, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted.add(nlog.Event{Source: "agent", Level: "error", Message: "next private event", OccurredAt: time.Now().Unix()})
	restarted.flush(t.Context(), client)
	raw, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, foreignData) || len(received) != 2 {
		t.Fatal("foreign queue overwritten or unpersisted batch uploaded", err)
	}
}

func TestCollectorUnchangedQueueFollowsProtectionTransitions(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	privacy.Bind(dir, nil, nil, true)
	scope := t.Name()
	nlog.ConfigureLogPolicies([]string{scope})
	t.Cleanup(func() { nlog.ConfigureLogPolicies(nil) })
	nlog.RegisterLogScope(scope, scope, filepath.Join(dir, "runtime.log.enc"), state)
	nlog.ApplyLogPolicy(scope, false)
	c := &collector{path: filepath.Join(dir, "pending.json"), scope: scope, key: state}
	c.add(nlog.Event{Source: "agent", Level: "warn", Message: "retained record", OccurredAt: time.Now().Unix()})
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	privacy.Bind(dir, state, nil, true)
	nlog.ApplyLogPolicy(scope, true)
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(c.path)
	if !privacy.IsEncrypted(raw) {
		t.Fatal("unchanged plaintext queue was not encrypted after policy change")
	}
	for range 5 {
		if err := c.save(); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, _ := os.ReadFile(c.path)
	if !bytes.Equal(raw, unchanged) {
		t.Fatal("unchanged private cache was re-encrypted")
	}
	nlog.ApplyLogPolicy(scope, false)
	if err := privacy.RestoreScopes(t.Context(), []string{dir}, state); err != nil {
		t.Fatal(err)
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(c.path)
	if privacy.IsEncrypted(raw) || !bytes.Contains(raw, []byte("retained record")) {
		t.Fatal("unchanged queue did not follow restoration")
	}
	privacy.EnableProtection(state)
	nlog.ApplyLogPolicy(scope, true)
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(c.path)
	if !privacy.IsEncrypted(raw) {
		t.Fatal("queue did not follow repeated enable")
	}
}
