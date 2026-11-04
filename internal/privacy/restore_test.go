package privacy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestoreWaitsForPendingWriteAndOldBindingStaysPlain(t *testing.T) {
	dir := t.TempDir()
	state, err := New([]Key{testKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	Bind(dir, state, nil, true)
	defer Bind(dir, nil, nil, true)
	path := filepath.Join(dir, "pending.json")
	final := []byte(`{"report_id":"last-write","traffic":[123,456]}`)
	release := LockFileWrite(path)
	sealed, err := Protect(path, final)
	if err != nil {
		release()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RestoreScopes(context.Background(), []string{dir}, state) }()
	select {
	case <-done:
		release()
		t.Fatal("restoration passed an in-flight write")
	case <-time.After(20 * time.Millisecond):
	}
	err = WriteRaw(path, sealed, 0o600)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, final) {
		t.Fatal("restoration lost the final in-flight report")
	}
	Bind(dir, state, nil, true)
	if err := WriteFile(path, final, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !bytes.Equal(raw, final) {
		t.Fatal("a stale binding encrypted restored data")
	}
	EnableProtection(state)
	if err := WriteFile(path, final, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if !IsEncrypted(raw) {
		t.Fatal("reenabling privacy did not restore encryption")
	}
}

func TestRestoreRejectsForeignKeyAndSkipsExternalSymlink(t *testing.T) {
	dir := t.TempDir()
	first, _ := New([]Key{testKey(t)})
	second, _ := New([]Key{testKey(t)})
	foreign, _ := second.Seal([]byte("other machine"), nil)
	path := filepath.Join(dir, "foreign.json")
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreScopes(context.Background(), []string{dir}, first); err == nil {
		t.Fatal("a different machine decrypted foreign data")
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, foreign) {
		t.Fatal("foreign encrypted file was overwritten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.json")
	if err := os.WriteFile(external, foreign, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if err := RestoreScopes(context.Background(), []string{dir}, first); err != nil {
		t.Fatal(err)
	}
	unchanged, _ = os.ReadFile(external)
	if !bytes.Equal(unchanged, foreign) {
		t.Fatal("restoration modified an external symlink target")
	}
}
