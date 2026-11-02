package service

import (
	"bytes"
	"os"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestEncryptedPendingReportPreservesTrafficAndRetryIdentity(t *testing.T) {
	dir := t.TempDir()
	privacytest.Bind(t, dir)
	store := newPendingReportStore(pendingTestConfig(dir, 7))
	batch := &pendingBatch{ID: "private-batch", pendingTraffic: pendingTraffic{Traffic: map[int][2]int64{9: {42, 84}}}}
	if err := store.save(batch, pendingTraffic{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(store.path)
	if !privacy.IsEncrypted(raw) || bytes.Contains(raw, []byte("panel.example")) {
		t.Fatal("待确认报告仍为明文")
	}
	restarted := newPendingReportStore(pendingTestConfig(dir, 7))
	loaded, _, err := restarted.load()
	if err != nil || loaded == nil || loaded.ID != batch.ID || loaded.Traffic[9] != batch.Traffic[9] {
		t.Fatal("加密恢复改变流量或重试编号")
	}
}
