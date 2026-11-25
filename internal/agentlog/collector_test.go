package agentlog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAgentLogRetryRestoreAndQueueBound(t *testing.T) {
	c := &collector{path: filepath.Join(t.TempDir(), "pending.json")}
	for i := 0; i < capacity+10; i++ {
		c.add(nlog.Event{Source: "agent", Level: "warn", Message: "test", OccurredAt: time.Now().Unix()})
	}
	if len(c.queue) != capacity || c.dropped != 10 {
		t.Fatal("queue must be bounded")
	}
	batch := c.next()
	if len(batch.Records) != 128 {
		t.Fatal("batch must be bounded")
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	restored := &collector{path: c.path}
	if err := restored.restore(); err != nil {
		t.Fatal(err)
	}
	if restored.next().ID != batch.ID {
		t.Fatal("restart must preserve batch identity")
	}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body panel.AgentLogBatch
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.ID != batch.ID {
			t.Error("retry must preserve batch identity")
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"batch_id": body.ID}})
	}))
	defer server.Close()
	client := panel.NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
	restored.flush(context.Background(), client)
	if restored.pending == nil {
		t.Fatal("failed response must retain pending batch")
	}
	restored.flush(context.Background(), client)
	if restored.pending != nil {
		t.Fatal("acknowledged batch must be removed")
	}
	final := &collector{path: c.path}
	if err := final.restore(); err != nil {
		t.Fatal(err)
	}
	if len(final.queue) != capacity-128 {
		t.Fatal("acknowledged batch must not return after restart")
	}
}
func TestAgentLogCorruptCachePreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	if err := writeInvalid(path); err != nil {
		t.Fatal(err)
	}
	c := &collector{path: path}
	if c.restore() == nil {
		t.Fatal("corruption must be reported")
	}
}
func TestAgentLogPreservesDiagnosticsBeforeQueueing(t *testing.T) {
	nlog.Init(io.Discard, 0, false)
	scope := "test-only-scope"
	c := &collector{}
	stop := nlog.Subscribe(scope, c.add)
	defer stop()
	message := "connect token=temporary-test-secret url=https://test.invalid/path?key=temporary-test-secret"
	nlog.Scoped(scope, 7, "node").Warn(message)
	batch := c.next()
	if batch == nil || batch.Records[0].NodeID != 7 {
		t.Fatal("node scope missing")
	}
	if batch.Records[0].Message != message {
		t.Fatal("diagnostic content changed before queueing")
	}
}

func TestCollectorSkipsUnchangedSnapshotsAndRetriesFailedSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	c := &collector{path: path}
	c.add(nlog.Event{Source: "agent", Level: "warn", Message: "first", OccurredAt: time.Now().Unix()})
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	for range 5 {
		if err := c.save(); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged cache was rewritten")
	}
	c.add(nlog.Event{Source: "agent", Level: "warn", Message: "second", OccurredAt: time.Now().Unix()})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.save(); err == nil {
		t.Fatal("failed save must report an error")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	restored := &collector{path: path}
	if err := restored.restore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.queue) != 2 {
		t.Fatal("failed save marked unsaved records as persisted")
	}
}

func TestCollectorConcurrentAddsAndSavesPreserveAllRecords(t *testing.T) {
	c := &collector{path: filepath.Join(t.TempDir(), "pending.json")}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 200 {
				c.add(nlog.Event{Source: "agent", Level: "warn", Message: "concurrent", OccurredAt: time.Now().Unix()})
			}
		})
	}
	for range 2 {
		workers.Go(func() {
			for range 10 {
				if err := c.save(); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	restored := &collector{path: c.path}
	if err := restored.restore(); err != nil {
		t.Fatal(err)
	}
	if len(restored.queue) != 800 {
		t.Fatalf("concurrent snapshot lost records: %d", len(restored.queue))
	}
}

func TestCollectorFiltersQueuedLevelsWithoutChangingPendingBatch(t *testing.T) {
	c := &collector{}
	for _, level := range []string{"info", "warn", "error"} {
		c.add(nlog.Event{Source: "agent", Level: level, Message: "fixed batch", OccurredAt: time.Now().Unix()})
	}
	pending := c.next()
	c.acceptLevel = func(level string) bool { return level == "warn" || level == "error" || level == "fatal" }
	c.add(nlog.Event{Source: "agent", Level: "info", Message: "queued before policy", OccurredAt: time.Now().Unix()})
	c.add(nlog.Event{Source: "xray", Level: "error", Event: "xray.crash", Message: "crash", OccurredAt: time.Now().Unix()})
	if got := c.next(); got != pending || len(got.Records) != 3 {
		t.Fatal("level update changed the fixed retry batch")
	}
	if len(c.queue) != 1 || c.queue[0].Event != "xray.crash" {
		t.Fatal("queued low-level logs were retained or crash event was lost")
	}
}

func BenchmarkCollectorUnchangedSave(b *testing.B) {
	for _, private := range []bool{false, true} {
		name := "plain"
		if private {
			name = "private"
		}
		b.Run(name, func(b *testing.B) {
			dir := b.TempDir()
			raw := bytes.Repeat([]byte{2}, 32)
			sum := sha256.Sum256(raw)
			state, err := privacy.New([]privacy.Key{{
				ID:    hex.EncodeToString(sum[:16]),
				Value: base64.StdEncoding.EncodeToString(raw),
			}})
			if err != nil {
				b.Fatal(err)
			}
			scope := b.Name()
			nlog.ConfigureLogPolicies([]string{scope})
			nlog.RegisterLogScope(scope, scope, filepath.Join(dir, "runtime.log.enc"), state)
			nlog.ApplyLogPolicy(scope, private)
			b.Cleanup(func() { nlog.ConfigureLogPolicies(nil) })
			c := &collector{path: filepath.Join(dir, "pending.json"), scope: scope, key: state}
			c.add(nlog.Event{Source: "agent", Level: "warn", Message: "unchanged warning", OccurredAt: time.Now().Unix()})
			if err := c.save(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := c.save(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
