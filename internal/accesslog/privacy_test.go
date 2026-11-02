package accesslog

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestEncryptedAccessCheckpointKeepsBatchIdentity(t *testing.T) {
	dir := t.TempDir()
	privacytest.Bind(t, dir)
	path := filepath.Join(dir, "pending.json")
	r := &Recorder{NodeID: 3}
	r.Enable(true)
	r.Add(7, "private.example.invalid", "", 443)
	batch, err := r.Next()
	if err != nil || batch == nil {
		t.Fatal("未建立访问记录批次")
	}
	if _, err := r.Checkpoint(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !privacy.IsEncrypted(raw) || bytes.Contains(raw, []byte("private.example.invalid")) {
		t.Fatal("访问记录未加密")
	}
	restarted := &Recorder{NodeID: 3}
	if err := restarted.Restore(path); err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Next()
	if err != nil || retry == nil || retry.ID != batch.ID || len(retry.Records) != 1 {
		t.Fatal("加密恢复改变幂等批次")
	}
}
