package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestIncrementalTrafficJournalRecoversAcrossCompactionAndBatchMoves(t *testing.T) {
	dir := t.TempDir()
	privacytest.Bind(t, dir)
	store := newPendingReportStore(pendingTestConfig(dir, 7))
	pending := pendingTraffic{Traffic: make(map[int][2]int64), Relay: map[int][2]int64{10: {1, 2}}, RelayUser: map[int]map[int][2]int64{1: {10: {1, 2}}}}
	for id := 1; id <= 10000; id++ {
		pending.Traffic[id] = [2]int64{int64(id), int64(id)}
	}
	if err := store.save(nil, pending); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.Stat(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 140; sequence++ {
		pending.Traffic[1] = [2]int64{int64(sequence + 1), 1}
		pending.Relay[10] = [2]int64{int64(sequence + 1), 2}
		pending.RelayUser[1][10] = [2]int64{int64(sequence + 1), 2}
		if err := store.save(nil, pending); err != nil {
			t.Fatal(err)
		}
		restarted := newPendingReportStore(pendingTestConfig(dir, 7))
		_, restored, err := restarted.load()
		if err != nil || restored.Traffic[1] != pending.Traffic[1] || restored.RelayUser[1][10] != pending.RelayUser[1][10] {
			t.Fatalf("增量恢复失败: %v", err)
		}
		store = restarted
	}
	entries, err := os.ReadDir(store.path + ".journal")
	if err != nil || len(entries) > 128 {
		t.Fatalf("检查点未限制日志数量: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(store.path+".journal", entry.Name()))
		if err != nil || !privacy.IsEncrypted(data) || int64(len(data)) >= baseline.Size()/10 {
			t.Fatal("增量仍完整落盘或没有加密")
		}
	}
	batch := &pendingBatch{ID: "confirmed-batch", pendingTraffic: pending}
	if err := store.save(batch, pendingTraffic{}); err != nil {
		t.Fatal(err)
	}
	restarted := newPendingReportStore(pendingTestConfig(dir, 7))
	got, rest, err := restarted.load()
	if err != nil || got == nil || got.ID != batch.ID || !rest.empty() || len(got.Traffic) != 10000 {
		t.Fatalf("批次移动恢复错误: %v", err)
	}
	if err := restarted.save(nil, pendingTraffic{}); err != nil {
		t.Fatal(err)
	}
	if files := pendingFiles(t, dir); len(files) != 0 {
		t.Fatalf("完成后遗留文件: %v", files)
	}
}

func TestUnchangedTrafficSkipsDiskAndCorruptJournalIsRetained(t *testing.T) {
	dir := t.TempDir()
	store := newPendingReportStore(pendingTestConfig(dir, 7))
	pending := pendingTraffic{Traffic: map[int][2]int64{1: {1, 2}}}
	if err := store.save(nil, pending); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(store.path)
	for range 10 {
		if err := store.save(nil, pending); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := os.Stat(store.path)
	if !os.SameFile(before, after) {
		t.Fatal("未变化内容仍被替换")
	}
	pending.Traffic[1] = [2]int64{2, 3}
	if err := store.save(nil, pending); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(store.path+".journal", fmt.Sprintf("%020d.json", store.journalSeq))
	if err := os.WriteFile(journal, []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newPendingReportStore(pendingTestConfig(dir, 7)).load(); err == nil {
		t.Fatal("损坏日志被静默忽略")
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatal("损坏记录未保留")
	}
}

func BenchmarkPendingJournalSingleUserChange(b *testing.B) {
	store := newPendingReportStore(pendingTestConfig(b.TempDir(), 7))
	pending := pendingTraffic{Traffic: make(map[int][2]int64)}
	for id := 1; id <= 10000; id++ {
		pending.Traffic[id] = [2]int64{1, 1}
	}
	if err := store.save(nil, pending); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for sequence := range b.N {
		pending.Traffic[1] = [2]int64{int64(sequence + 2), 1}
		if err := store.save(nil, pending); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	var checkpoint pendingReportFile
	data, _ := privacy.ReadFile(store.path)
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		b.Fatal(err)
	}
}
