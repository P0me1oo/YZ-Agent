package agentlog

import (
	"context"
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
func TestAgentLogDoesNotRecurseAndSanitizes(t *testing.T) {
	nlog.Init(io.Discard, 0, false)
	scope := "test-only-scope"
	c := &collector{}
	stop := nlog.Subscribe(scope, c.add)
	defer stop()
	nlog.Scoped(scope, 7, "node").Warn("connect token=temporary-test-secret url=https://test.invalid/path?key=temporary-test-secret")
	batch := c.next()
	if batch == nil || batch.Records[0].NodeID != 7 {
		t.Fatal("node scope missing")
	}
	if batch.Records[0].Message == "" || containsSecret(batch.Records[0].Message) {
		t.Fatal("secret must be removed before queueing")
	}
}
