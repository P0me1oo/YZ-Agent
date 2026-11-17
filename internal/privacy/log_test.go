package privacy

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func logKey(t *testing.T) Key {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return Key{ID: hex.EncodeToString(sum[:16]), Value: base64.StdEncoding.EncodeToString(raw)}
}

func TestLogWriterConcurrentRecordsRemainComplete(t *testing.T) {
	state, err := New([]Key{logKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.log.enc")
	writer := NewLogWriter(path, state)
	t.Cleanup(func() { _ = writer.Close() })
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 20 {
				if _, err := writer.Write([]byte("concurrent private record\n")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(decoded.Bytes(), []byte("concurrent private record\n")) != 160 {
		t.Fatal("concurrent writes lost or mixed records")
	}
}

func TestLogWriterEncryptsRotatesAndRecoversHistoricalKeys(t *testing.T) {
	oldKey, newKey := logKey(t), logKey(t)
	old, err := New([]Key{oldKey})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.log.enc")
	writer := NewLogWriter(path, old)
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("old private destination\n")); err != nil {
		t.Fatal(err)
	}
	current, err := New([]Key{newKey, oldKey})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writer = NewLogWriter(path, current)
	if _, err := writer.Write([]byte("new private destination\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private destination")) {
		t.Fatal("plaintext persisted")
	}
	var decoded bytes.Buffer
	if err := current.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if decoded.String() != "old private destination\nnew private destination\n" {
		t.Fatal("records lost across key rotation")
	}
	foreign, _ := New([]Key{logKey(t)})
	if err := foreign.ReadLog(&bytes.Buffer{}, bytes.NewReader(raw)); err == nil {
		t.Fatal("foreign machine read logs")
	}
	writer.maxBytes = int64(len(raw))
	writer.keep = 2
	for range 4 {
		if _, err := writer.Write([]byte("rotated private record\n")); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(path + "*")
	if len(files) != 3 {
		t.Fatalf("unbounded rotation: %d files", len(files))
	}
	for _, name := range files {
		info, _ := os.Stat(name)
		if info.Mode().Perm() != 0o600 {
			t.Fatal("log permissions too broad")
		}
		raw, _ := os.ReadFile(name)
		if err := current.ReadLog(&bytes.Buffer{}, bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogWriterPreservesCorruptAndPlaintextFiles(t *testing.T) {
	state, _ := New([]Key{logKey(t)})
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"plaintext", []byte("existing private plaintext")},
		{"partial", append([]byte(LogMagic), 0, 0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.log.enc")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewLogWriter(path, state).Write([]byte("secret")); err == nil {
				t.Fatal("corrupt log overwritten")
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, test.data) {
				t.Fatal("original file lost")
			}
		})
	}
}

func TestLogWriterReopensReplacedFilesAndRepairsPermissions(t *testing.T) {
	state, err := New([]Key{logKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "runtime.log.enc")
	writer := NewLogWriter(path, state)
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("before external rotation\n")); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".external"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("after external rotation\n")); err != nil {
		t.Fatal(err)
	}
	preserved, _ := os.ReadFile(path + ".external")
	if !bytes.Equal(old, preserved) {
		t.Fatal("reused handle wrote into the externally moved file")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("replacement permissions were not repaired")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal("closing an idle handle must be harmless", err)
	}
	if _, err := writer.Write([]byte("after reopen\n")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if decoded.String() != "after external rotation\nafter reopen\n" {
		t.Fatal("reopen lost or mixed records")
	}
}

func TestLogWriterPreservesSameSizeExternalCorruption(t *testing.T) {
	state, _ := New([]Key{logKey(t)})
	path := filepath.Join(t.TempDir(), "runtime.log.enc")
	writer := NewLogWriter(path, state)
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := writer.Write([]byte("original record\n")); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("must not append\n")); err == nil {
		t.Fatal("same-size corruption bypassed validation")
	}
	preserved, _ := os.ReadFile(path)
	if !bytes.Equal(raw, preserved) {
		t.Fatal("corrupt original file was changed")
	}
}

func BenchmarkLogWriter(b *testing.B) {
	for _, size := range []int{128, 1024} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			raw := bytes.Repeat([]byte{1}, 32)
			sum := sha256.Sum256(raw)
			state, err := New([]Key{{
				ID:    hex.EncodeToString(sum[:16]),
				Value: base64.StdEncoding.EncodeToString(raw),
			}})
			if err != nil {
				b.Fatal(err)
			}
			writer := NewLogWriter(filepath.Join(b.TempDir(), "runtime.log.enc"), state)
			b.Cleanup(func() { _ = writer.Close() })
			data := bytes.Repeat([]byte("x"), size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if _, err := writer.Write(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
