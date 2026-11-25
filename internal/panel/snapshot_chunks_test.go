package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/gorilla/websocket"
)

func snapshotFixture(data []byte, id string, node int) (snapshotManifest, []snapshotChunk) {
	digest := sha256.Sum256(data)
	manifest := snapshotManifest{ID: id, SHA256: fmt.Sprintf("%x", digest), Bytes: len(data), Chunks: (len(data) + snapshotChunkSize - 1) / snapshotChunkSize}
	control := json.RawMessage(fmt.Sprintf(`{"epoch":"test-epoch","sequence":9007199254740993,"node_id":%d,"config":{"protocol":"vless","server_port":"443"},"users":[]}`, node))
	chunks := make([]snapshotChunk, manifest.Chunks)
	for i := range chunks {
		chunks[i] = snapshotChunk{snapshotManifest: manifest, Index: i, Chunk: data[i*snapshotChunkSize : min(len(data), (i+1)*snapshotChunkSize)]}
		if i == 0 {
			chunks[i].Control = control
		}
	}
	return manifest, chunks
}

func TestSnapshotAssemblerInterleavingIsAtomic(t *testing.T) {
	data := []byte(`[{"id":"1","uuid":"test","unused":"` + strings.Repeat("x", snapshotChunkSize) + `"}]`)
	_, left := snapshotFixture(data, "left", 1)
	_, right := snapshotFixture(data, "right", 2)
	a := &snapshotAssembler{}
	now := time.Now()
	for _, chunk := range []snapshotChunk{left[0], right[0]} {
		control, users, err := a.accept(chunk, now)
		if err != nil || control != nil || users != nil {
			t.Fatal("partial snapshot exposed", err)
		}
	}
	for _, chunk := range []snapshotChunk{right[1], left[1]} {
		control, users, err := a.accept(chunk, now)
		if err != nil || control == nil || !bytes.Equal(data, users) {
			t.Fatal(err)
		}
	}
	if a.reserved != 0 || a.controlReserved != 0 || len(a.pending) != 0 {
		t.Fatal("finished snapshot retained memory")
	}
}

func TestSnapshotAssemblerRejectsRepeatedCompletedSnapshot(t *testing.T) {
	_, chunks := snapshotFixture([]byte(`[{"id":1}]`), "complete", 1)
	a := &snapshotAssembler{}
	if _, _, err := a.accept(chunks[0], time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.accept(chunks[0], time.Now()); err == nil {
		t.Fatal("completed snapshot replayed")
	}
}

func TestSnapshotAssemblerRejectsMalformedSequences(t *testing.T) {
	data := []byte(`[{"id":"1","unused":"` + strings.Repeat("x", snapshotChunkSize) + `"}]`)
	for _, name := range []string{"out-of-order", "duplicate", "digest", "mixed", "expired", "budget", "control-budget", "wrong-size", "same-node"} {
		t.Run(name, func(t *testing.T) {
			_, chunks := snapshotFixture(data, "one", 1)
			a := &snapshotAssembler{}
			now := time.Now()
			if name == "out-of-order" {
				if _, _, err := a.accept(chunks[1], now); err == nil {
					t.Fatal("accepted later chunk first")
				}
				return
			}
			if name == "digest" {
				chunks[0].SHA256 = strings.Repeat("a", 64)
				chunks[1].SHA256 = chunks[0].SHA256
			}
			if name == "budget" {
				a.reserved = snapshotMaxBytes
				if _, _, err := a.accept(chunks[0], now); err == nil {
					t.Fatal("memory budget exceeded")
				}
				return
			}
			if name == "control-budget" {
				a.controlReserved = snapshotMaxControl
				if _, _, err := a.accept(chunks[0], now); err == nil {
					t.Fatal("control memory budget exceeded")
				}
				return
			}
			if _, _, err := a.accept(chunks[0], now); err != nil {
				t.Fatal(err)
			}
			next := chunks[1]
			switch name {
			case "duplicate":
				next = chunks[0]
			case "mixed":
				next.SHA256 = strings.Repeat("b", 64)
			case "expired":
				now = now.Add(snapshotTTL)
			case "wrong-size":
				next.Chunk = next.Chunk[:len(next.Chunk)-1]
			case "same-node":
				next = chunks[0]
				next.ID = "another"
			}
			if _, _, err := a.accept(next, now); err == nil {
				t.Fatal("malformed chunk accepted")
			}
			if name == "expired" && (len(a.pending) != 0 || a.reserved != 0 || a.controlReserved != 0) {
				t.Fatal("expired snapshot retained memory")
			}
		})
	}
}

func TestChunkedHTTPBeyondLegacyResponseLimit(t *testing.T) {
	data := []byte(`[{"id":"7","uuid":"dedicated-test","unused":"` + strings.Repeat("x", (32<<20)+1) + `"}]`)
	manifest, chunks := snapshotFixture(data, "http-test", 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-YZ-Snapshot-Chunks") != "1" || r.URL.Query().Get("machine_id") != "2" || r.URL.Query().Get("node_id") != "3" || r.URL.Query().Get("token") != "test-only" {
			t.Error("missing capability or per-node authentication")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/api/v2/server/realtime/sync" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"control": map[string]any{"epoch": "test-epoch", "sequence": uint64(9007199254740993), "node_id": 3, "config": map[string]any{"protocol": "vless"}, "users": []any{}, "users_snapshot": manifest}}})
			return
		}
		index, err := strconv.Atoi(r.URL.Query().Get("index"))
		if r.URL.Path != "/api/v2/server/snapshot/chunk" || r.URL.Query().Get("id") != manifest.ID || err != nil || index < 0 || index >= len(chunks) {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": manifest.ID, "index": index, "data": chunks[index].Chunk})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 2, NodeID: 3, Token: "test-only"})
	state, err := client.GetControlState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Control.Users) != 1 || state.Control.Users[0].ID != 7 || state.Control.Sequence != 9007199254740993 {
		t.Fatal("snapshot decoding lost user or sequence")
	}
}

func TestChunkedWSSnapshotBeyondMessageLimit(t *testing.T) {
	data := []byte(`[{"id":"1","uuid":"dedicated-test","unused":"` + strings.Repeat("x", (10<<20)+1) + `"}]`)
	_, chunks := snapshotFixture(data, "ws-test", 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("snapshot_chunks") != "1" {
			t.Error("missing capability")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(wsMessage{Event: "auth.success"})
		for _, chunk := range chunks {
			raw, _ := json.Marshal(chunk)
			if conn.WriteJSON(wsMessage{Event: "sync.snapshot.chunk", Data: raw}) != nil {
				return
			}
		}
		_ = conn.WriteJSON(wsMessage{Event: "sync.ready"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	events := make(chan WSEvent, 2)
	ws := NewWSClient("ws"+strings.TrimPrefix(server.URL, "http"), "test", 1, WSClientConfig{Realtime: true}, func(e WSEvent) { events <- e }, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ws.connect(ctx) }()
	select {
	case event := <-events:
		if event.Type != WSEventSyncSnapshot || len(event.Users) != 1 || event.Users[0].ID != 1 || event.Version.Sequence != 9007199254740993 {
			t.Fatal("invalid assembled event")
		}
	case err := <-done:
		t.Fatal("connection ended before snapshot", err)
	case <-time.After(10 * time.Second):
		t.Fatal("missing assembled event")
	}
	cancel()
	<-done
	if len(events) != 0 {
		t.Fatal("partial or duplicate snapshot emitted")
	}
}

func TestWSReconnectCannotCompleteOldSnapshot(t *testing.T) {
	data := []byte(`[{"id":1,"unused":"` + strings.Repeat("x", snapshotChunkSize) + `"}]`)
	_, chunks := snapshotFixture(data, "old", 1)
	var connections atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(wsMessage{Event: "auth.success"})
		index := int(connections.Add(1)) - 1
		if index > 1 {
			return
		}
		raw, _ := json.Marshal(chunks[index])
		_ = conn.WriteJSON(wsMessage{Event: "sync.snapshot.chunk", Data: raw})
	}))
	defer server.Close()
	events := 0
	ws := NewWSClient("ws"+strings.TrimPrefix(server.URL, "http"), "test", 1, WSClientConfig{}, func(WSEvent) { events++ }, nil, nil)
	for range 2 {
		if err := ws.connect(t.Context()); err == nil {
			t.Fatal("partial snapshot connection unexpectedly succeeded")
		}
	}
	if events != 0 {
		t.Fatal("reconnect joined old partial data")
	}
}

func TestChunkedHTTPRejectsWrongIdentityAndDigestWithoutPartialUsers(t *testing.T) {
	for _, wrongID := range []bool{false, true} {
		t.Run(map[bool]string{false: "digest", true: "identity"}[wrongID], func(t *testing.T) {
			manifest, chunks := snapshotFixture([]byte(`[{"id":7}]`), "expected", 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id, body := manifest.ID, append([]byte{}, chunks[0].Chunk...)
				if wrongID {
					id = "other"
				} else {
					body[len(body)-3] = '8'
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "index": 0, "data": body})
			}))
			defer server.Close()
			client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
			users, err := client.fetchSnapshotUsers(t.Context(), manifest)
			if err == nil || users != nil {
				t.Fatal("unverified users returned", users, err)
			}
		})
	}
}

func TestChunkedHTTPSharedBudgetWaitCanBeCancelled(t *testing.T) {
	client := NewClient(config.PanelConfig{})
	node := client.ForNode(1)
	client.snapshotSlots <- struct{}{}
	defer func() { <-client.snapshotSlots }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	manifest, _ := snapshotFixture([]byte(`[]`), "cancelled", 1)
	if _, err := node.fetchSnapshotUsers(ctx, manifest); err != context.Canceled {
		t.Fatal(err)
	}
}
