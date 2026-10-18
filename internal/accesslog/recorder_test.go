package accesslog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHostOnlyAndIPFallback(t *testing.T) {
	for input, want := range map[string]string{"EXAMPLE.COM.": "example.com", "2001:0db8::1": "2001:db8::1", "::ffff:203.0.113.1": "203.0.113.1", "例子.测试": "xn--fsqu00a.xn--0zwm56d", "https://example.com/path": "", "example.com/path": "", "user@example.com": "", "example.com?key=x": "", "example.com\n": "", "999.0.0.1": "", "fe80::1%eth0": "", "[::1]": ""} {
		if got := Host(input); got != want {
			t.Errorf("Host(%q) = %q, want %q", input, got, want)
		}
	}
	r := &Recorder{}
	r.Add(1, "example.test", "", 443)
	if batch, _ := r.Next(); batch != nil {
		t.Fatal("未启用时不应采集")
	}
	r.Enable(true)
	r.Add(1, "https://invalid.test/path", "203.0.113.8", 443)
	r.Add(0, "example.test", "", 443)
	r.Add(1, "example.test", "", 0)
	batch, err := r.Next()
	if err != nil || len(batch.Records) != 1 || batch.Records[0].Destination != "203.0.113.8" {
		t.Fatal("回退或身份校验失败")
	}
	encoded, _ := json.Marshal(batch.Records[0])
	var fields map[string]any
	_ = json.Unmarshal(encoded, &fields)
	if len(fields) != 4 {
		t.Fatal("不应采集其他连接信息")
	}
}

func TestRetryCheckpointRestoreAndNewConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	r := &Recorder{NodeID: 3}
	r.Enable(true)
	for i := 0; i < MaxBatch+3; i++ {
		r.AddForNode(9, 7, "example.test", "", 443)
	}
	first, _ := r.Next()
	r.Add(8, "", "2001:db8::1", 80)
	again, _ := r.Next()
	if !reflect.DeepEqual(first, again) || len(first.Records) != MaxBatch {
		t.Fatal("重试批次改变")
	}
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	restored := &Recorder{}
	if err := restored.Restore(path); err != nil {
		t.Fatal(err)
	}
	got, _ := restored.Next()
	if !reflect.DeepEqual(first, got) {
		t.Fatal("重启未保留批次和内容")
	}
	if got.Records[0].NodeID != 9 {
		t.Fatal("恢复后落地归属丢失")
	}
	restored.Ack("错误确认")
	got, _ = restored.Next()
	if got.ID != first.ID {
		t.Fatal("错误确认丢弃了记录")
	}
	restored.Ack(first.ID)
	next, _ := restored.Next()
	if next.ID == first.ID || len(next.Records) != 4 || next.Records[3].UserID != 8 || next.Records[3].NodeID != 3 {
		t.Fatal("新连接丢失或混入旧批次")
	}
	restored.Ack(next.ID)
	if err := restored.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("已确认数据未清理")
	}
}

func TestConcurrentBoundAndExpiry(t *testing.T) {
	r := &Recorder{}
	r.Enable(true)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < MaxRecords/8+1; j++ {
				r.Add(1, "example.test", "", 443)
			}
		})
	}
	wg.Wait()
	if r.Dropped() != 8 {
		t.Fatal("队列上限或丢弃计数不正确")
	}
	batch, _ := r.Next()
	if batch == nil {
		t.Fatal("缺少批次")
	}
	r.mu.Lock()
	for _, batch := range r.queue {
		for i := range batch.Records {
			batch.Records[i].ConnectedAt = time.Now().Add(-Retention - time.Second).Unix()
		}
	}
	for i := range r.pending.Records {
		r.pending.Records[i].ConnectedAt = time.Now().Add(-Retention - time.Second).Unix()
	}
	r.mu.Unlock()
	if batch, _ = r.Next(); batch != nil {
		t.Fatal("过期记录未清理")
	}
}

func TestCorruptStoreAndWriteFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pending.json")
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{}
	r.Enable(true)
	if err := r.Restore(path); err == nil {
		t.Fatal("损坏缓存被接受")
	}
	r.Add(1, "example.test", "", 443)
	batch, _ := r.Next()
	if err := r.Save(filepath.Join(path, "child")); err == nil {
		t.Fatal("写入失败没有上报")
	}
	got, _ := r.Next()
	if got.ID != batch.ID {
		t.Fatal("写入失败丢失了批次")
	}
}
