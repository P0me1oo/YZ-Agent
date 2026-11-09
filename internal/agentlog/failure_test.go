package agentlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestUploaderFailureLogsWithoutRecursionAndPreservesPending(t *testing.T) {
	var output bytes.Buffer
	nlog.ConfigureLogPolicies(nil)
	nlog.Init(&output, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	scope := t.Name()
	c := &collector{path: filepath.Join(t.TempDir(), "queue"), logger: nlog.Scoped(scope, 7, "agent-log")}
	stop := nlog.Subscribe(scope, c.add)
	defer stop()
	c.add(nlog.Event{Source: "agent", Level: "error", Message: "original", OccurredAt: time.Now().Unix()})
	var ack atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch panel.AgentLogBatch
		json.NewDecoder(r.Body).Decode(&batch)
		if !ack.Load() {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"batch_id": batch.ID}})
	}))
	defer server.Close()
	client := panel.NewClient(config.PanelConfig{URL: server.URL, NodeID: 7})
	c.flush(context.Background(), client)
	id := c.pending.ID
	c.flush(context.Background(), client)
	if strings.Count(output.String(), "Agent 日志上报未确认") != 1 || c.pending.ID != id || len(c.queue) != 0 {
		t.Fatal("failed upload lost batch or generated recursive logs")
	}
	ack.Store(true)
	c.flush(context.Background(), client)
	if c.pending != nil {
		t.Fatal("recovery did not acknowledge pending batch")
	}
	// A directory at the final path makes rename fail even when testing as root.
	if err := os.Remove(c.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.path, 0700); err != nil {
		t.Fatal(err)
	}
	c.add(nlog.Event{Source: "agent", Level: "error", Message: "next", OccurredAt: time.Now().Unix()})
	c.flush(context.Background(), client)
	if c.pending == nil || !strings.Contains(output.String(), "Agent 日志缓存保存失败") {
		t.Fatal("disk failure must retain batch and explain missing upload")
	}
}

func TestUploadTimeoutIsNotMistakenForShutdown(t *testing.T) {
	var output bytes.Buffer
	nlog.ConfigureLogPolicies(nil)
	nlog.Init(&output, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	c := &collector{path: filepath.Join(t.TempDir(), "queue")}
	c.add(nlog.Event{Source: "agent", Level: "error", Message: "original", OccurredAt: time.Now().Unix()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c.flush(ctx, panel.NewClient(config.PanelConfig{URL: server.URL, NodeID: 7}))
	if c.pending == nil || !strings.Contains(output.String(), "Agent 日志上报未确认") {
		t.Fatal("upload timeout silently dropped")
	}
}

type diagnosticWriter chan string

func (w diagnosticWriter) Write(data []byte) (int, error) {
	select {
	case w <- string(data):
	default:
	}
	return len(data), nil
}

func TestCorruptCacheReportsAfterPolicyAndIsNeverOverwritten(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"log_policy":{"version":1,"disabled":false},"agent_log":{"version":1}}`))
	}))
	defer server.Close()
	cfg := &config.Config{Panel: config.PanelConfig{URL: server.URL, NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	path := filepath.Join(cfg.Kernel.ConfigDir, fmt.Sprintf("pending-agent-%x.json", sha256.Sum256([]byte(cfg.LogScope()))))
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	nlog.ConfigureLogPolicies([]string{nlog.LogPolicyKey(server.URL, 0, 7)})
	output := make(diagnosticWriter, 4)
	nlog.Init(output, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false); nlog.ConfigureLogPolicies(nil) })
	stop := Start(context.Background(), cfg)
	defer stop()
	select {
	case text := <-output:
		if !strings.Contains(text, "Agent 日志缓存恢复失败") {
			t.Fatal(text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("corrupt cache remained silently disabled")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "broken" {
		t.Fatal("corrupt cache was overwritten", err)
	}
}
