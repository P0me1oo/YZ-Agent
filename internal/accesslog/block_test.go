package accesslog

import (
	"path/filepath"
	"testing"
)

func TestBlockQueueUnknownUserRestoreAndBounds(t *testing.T) {
	r := &Recorder{NodeID: 7}
	r.Enable(true)
	r.AddBlock(0, 31, "EXAMPLE.test", "", 443, "tcp")
	r.AddBlock(9, 32, "", "2001:db8::1", 53, "udp")
	for _, rule := range []int{0, -1} {
		r.AddBlock(1, rule, "example.test", "", 443, "tcp")
	}
	r.AddBlock(1, 31, "https://example.test/private", "", 443, "tcp")
	r.AddBlock(1, 31, "example.test", "", 443, "quic")
	path := filepath.Join(t.TempDir(), "blocks.json")
	batch, err := r.Next()
	if err != nil || batch == nil {
		t.Fatal(err)
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := &Recorder{}
	if err := restored.Restore(path); err != nil {
		t.Fatal(err)
	}
	got, err := restored.Next()
	if err != nil || got == nil || got.ID != batch.ID || len(got.Records) != 2 {
		t.Fatalf("恢复失败: %+v %v", got, err)
	}
	if got.Records[0].UserID != 0 || got.Records[0].RuleID != 31 || got.Records[0].Destination != "example.test" || got.Records[1].Network != "udp" {
		t.Fatal(got.Records)
	}
	restored.Ack(got.ID)
	if next, _ := restored.Next(); next != nil {
		t.Fatal("确认后未移除批次")
	}
	for i := 0; i < MaxRecords+2; i++ {
		r.AddBlock(1, 31, "example.test", "", 443, "tcp")
	}
	if r.Dropped() != 4 {
		t.Fatal("超量队列没有按上限丢弃")
	}
}
