package panel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	snapshotChunkSize  = 512 << 10
	snapshotMaxBytes   = 128 << 20
	snapshotMaxControl = 4 << 20
	snapshotTTL        = 120 * time.Second
	snapshotMaxPending = 256
)

type snapshotManifest struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Chunks int    `json:"chunks"`
}

func (m snapshotManifest) validate() error {
	if len(m.ID) == 0 || len(m.ID) > 128 || m.Bytes <= 0 || m.Bytes > snapshotMaxBytes || m.Chunks != (m.Bytes+snapshotChunkSize-1)/snapshotChunkSize {
		return errors.New("invalid chunked snapshot metadata")
	}
	if digest, err := hex.DecodeString(m.SHA256); err != nil || len(digest) != sha256.Size {
		return errors.New("invalid chunked snapshot digest")
	}
	return nil
}

func (m snapshotManifest) chunkBytes(index int) int {
	if index < 0 || index >= m.Chunks {
		return 0
	}
	return min(snapshotChunkSize, m.Bytes-index*snapshotChunkSize)
}

func (m snapshotManifest) verify(data []byte) error {
	if len(data) != m.Bytes {
		return errors.New("incomplete chunked snapshot")
	}
	digest := sha256.Sum256(data)
	expected, _ := hex.DecodeString(m.SHA256)
	if !bytes.Equal(digest[:], expected) {
		return errors.New("chunked snapshot digest mismatch")
	}
	return nil
}

type snapshotChunk struct {
	snapshotManifest
	Index   int             `json:"index"`
	Chunk   []byte          `json:"chunk"`
	Control json.RawMessage `json:"control,omitempty"`
}

type pendingSnapshot struct {
	manifest snapshotManifest
	control  json.RawMessage
	users    []byte
	next     int
	nodeID   int
	deadline time.Time
}

// The WebSocket reader owns this assembler. A fresh instance is created for
// each connection so no reconnect can complete an older partial snapshot.
type snapshotAssembler struct {
	pending         map[string]*pendingSnapshot
	completed       map[string]time.Time
	reserved        int
	controlReserved int
}

func (a *snapshotAssembler) expire(now time.Time) bool {
	for id, deadline := range a.completed {
		if !now.Before(deadline) {
			delete(a.completed, id)
		}
	}
	expired := false
	for id, p := range a.pending {
		if !now.Before(p.deadline) {
			a.reserved -= p.manifest.Bytes
			a.controlReserved -= len(p.control)
			delete(a.pending, id)
			expired = true
		}
	}
	return expired
}

func (a *snapshotAssembler) accept(chunk snapshotChunk, now time.Time) (json.RawMessage, []byte, error) {
	if a.expire(now) {
		return nil, nil, errors.New("chunked snapshot expired")
	}
	if err := chunk.snapshotManifest.validate(); err != nil {
		return nil, nil, err
	}
	if len(chunk.Chunk) != chunk.chunkBytes(chunk.Index) || len(chunk.Chunk) == 0 {
		return nil, nil, errors.New("invalid snapshot chunk size or index")
	}
	if a.pending == nil {
		a.pending = map[string]*pendingSnapshot{}
	}
	if _, seen := a.completed[chunk.ID]; seen {
		return nil, nil, errors.New("snapshot already completed")
	}
	p := a.pending[chunk.ID]
	if p == nil {
		if chunk.Index != 0 || len(chunk.Control) == 0 {
			return nil, nil, errors.New("snapshot must begin with control and first chunk")
		}
		if len(a.pending) >= snapshotMaxPending || chunk.Bytes > snapshotMaxBytes-a.reserved || len(chunk.Control) > snapshotMaxControl-a.controlReserved {
			return nil, nil, errors.New("chunked snapshot memory budget exceeded")
		}
		var header struct {
			StateVersion
			NodeID int `json:"node_id"`
			Config struct {
				Protocol string `json:"protocol"`
			} `json:"config"`
		}
		if err := json.Unmarshal(chunk.Control, &header); err != nil || header.Epoch == "" || header.NodeID <= 0 || header.Config.Protocol == "" {
			return nil, nil, errors.New("invalid chunked snapshot control")
		}
		for _, other := range a.pending {
			if other.nodeID == header.NodeID {
				return nil, nil, errors.New("overlapping snapshots for the same node")
			}
		}
		p = &pendingSnapshot{manifest: chunk.snapshotManifest, control: chunk.Control, users: make([]byte, 0, chunk.Bytes), nodeID: header.NodeID, deadline: now.Add(snapshotTTL)}
		a.pending[chunk.ID] = p
		a.reserved += chunk.Bytes
		a.controlReserved += len(chunk.Control)
	} else if chunk.Index == 0 || len(chunk.Control) != 0 {
		return nil, nil, errors.New("duplicate snapshot control")
	}
	if p.manifest != chunk.snapshotManifest || chunk.Index != p.next {
		return nil, nil, errors.New("mixed, duplicate or out-of-order snapshot chunks")
	}
	p.users = append(p.users, chunk.Chunk...)
	p.next++
	if p.next != p.manifest.Chunks {
		return nil, nil, nil
	}
	delete(a.pending, chunk.ID)
	a.reserved -= p.manifest.Bytes
	a.controlReserved -= len(p.control)
	if err := p.manifest.verify(p.users); err != nil {
		return nil, nil, err
	}
	if a.completed == nil {
		a.completed = map[string]time.Time{}
	}
	if len(a.completed) >= 4096 {
		return nil, nil, errors.New("snapshot completion rate exceeded")
	}
	a.completed[chunk.ID] = now.Add(snapshotTTL)
	return p.control, p.users, nil
}

func decodeSnapshotUsers(data []byte) ([]User, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('[') {
		return nil, errors.New("snapshot users must be an array")
	}
	users := []User{}
	for decoder.More() {
		var raw map[string]interface{}
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		if raw == nil {
			return nil, errors.New("invalid snapshot user")
		}
		var user User
		if err := decodeWeakRaw(raw, &user); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing snapshot users data")
	}
	return users, nil
}

func (c *Client) fetchSnapshotUsers(ctx context.Context, manifest snapshotManifest) ([]User, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTTL)
	defer cancel()
	// Node clients share the machine transport and this slot. Concurrent
	// startup must not allocate a separate 128 MiB transfer for every node.
	if c.snapshotSlots != nil {
		select {
		case c.snapshotSlots <- struct{}{}:
			defer func() { <-c.snapshotSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	data := make([]byte, 0, manifest.Bytes)
	for index := 0; index < manifest.Chunks; index++ {
		query := url.Values{"id": {manifest.ID}, "index": {strconv.Itoa(index)}}
		response, err := c.doRequestContext(ctx, http.MethodGet, "/api/v2/server/snapshot/chunk?"+query.Encode(), nil, "")
		if err != nil {
			return nil, err
		}
		var chunk struct {
			ID    string `json:"id"`
			Index int    `json:"index"`
			Data  []byte `json:"data"`
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, &RemoteError{Code: response.StatusCode}
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&chunk)
		drainAndClose(response.Body)
		if err != nil {
			return nil, fmt.Errorf("decode snapshot chunk: %w", err)
		}
		if chunk.ID != manifest.ID || chunk.Index != index || len(chunk.Data) != manifest.chunkBytes(index) {
			return nil, errors.New("snapshot chunk identity or size mismatch")
		}
		data = append(data, chunk.Data...)
	}
	if err := manifest.verify(data); err != nil {
		return nil, err
	}
	return decodeSnapshotUsers(data)
}

func (w *WSClient) handleSnapshotChunk(raw json.RawMessage, assembler *snapshotAssembler) error {
	var chunk snapshotChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return err
	}
	control, users, err := assembler.accept(chunk, time.Now())
	if err != nil || control == nil {
		return err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(control, &fields); err != nil {
		return err
	}
	delete(fields, "users")
	var snapshot ControlSnapshot
	if err := decodeWeakRaw(fields, &snapshot); err != nil {
		return err
	}
	if err := json.Unmarshal(control, &snapshot.StateVersion); err != nil {
		return err
	}
	snapshot.Users, err = decodeSnapshotUsers(users)
	if err != nil {
		return err
	}
	if w.cfg.MachineID == 0 && snapshot.NodeID != w.nodeID {
		return errors.New("snapshot belongs to another node")
	}
	if w.onEvent != nil {
		w.onEvent(WSEvent{Type: WSEventSyncSnapshot, Version: snapshot.StateVersion, NodeID: snapshot.NodeID, Config: &snapshot.Config, Users: snapshot.Users})
	}
	return nil
}
