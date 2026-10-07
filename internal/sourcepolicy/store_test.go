package sourcepolicy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreRefreshFailureKeepsLastValidDatabase(t *testing.T) {
	var calls atomic.Int32
	var payload atomic.Value
	payload.Store(testData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(payload.Load().(string)))
	}))
	defer server.Close()
	s := &Store{dir: t.TempDir(), url: server.URL, client: server.Client()}
	ctx := context.Background()
	first, err := s.Get(ctx)
	if err != nil || !first.Ready() {
		t.Fatalf("首次下载失败: %v", err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Get(ctx) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("重复和并发请求重复下载")
	}
	payload.Store("<html>not a database</html>")
	s.nextCheck = time.Time{}
	kept, err := s.Get(ctx)
	if err == nil || kept.Revision != first.Revision {
		t.Fatal("损坏更新未保留旧版本")
	}
	saved, err := os.ReadFile(filepath.Join(s.dir, "cn-source.txt"))
	if err != nil || string(saved) != testData {
		t.Fatal("损坏更新覆盖了磁盘数据")
	}
	_, _ = s.Get(ctx)
	if calls.Load() != 2 {
		t.Fatal("失败后未退避")
	}
	payload.Store(testData + "203.0.113.0/24\n")
	s.nextCheck = time.Time{}
	updated, err := s.Get(ctx)
	if err != nil || updated.Revision == first.Revision {
		t.Fatalf("有效更新未生效: %v", err)
	}
	// 模拟进程重启，磁盘缓存应直接可用，不依赖网络。
	restarted := &Store{dir: s.dir, url: server.URL, client: server.Client()}
	got, err := restarted.Get(ctx)
	if err != nil || got.Revision != updated.Revision || calls.Load() != 3 {
		t.Fatal("重启没有复用有效缓存")
	}
}

func TestStoreInitialFailureDoesNotPublishEmptyData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	s := &Store{dir: t.TempDir(), url: server.URL, client: server.Client()}
	snapshot, err := s.Get(context.Background())
	if err == nil || snapshot.Ready() {
		t.Fatal("首次失败不应变成可用的空数据")
	}
	if _, err := os.Stat(filepath.Join(s.dir, "cn-source.txt")); !os.IsNotExist(err) {
		t.Fatal("首次失败不应写入缓存")
	}
}
