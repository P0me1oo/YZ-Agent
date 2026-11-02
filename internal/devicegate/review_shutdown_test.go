package devicegate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestStopDeadlineCoversBlockedCloseAndPreservesReferences(t *testing.T) {
	m := testManager(t, &testRemote{})
	p := acquire(t, m, 1, "8.8.8.8")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	p.Bind(func() { close(entered); <-release; p.Release() })
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("退出未返回总期限错误: %v", err)
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatal("退出期限未覆盖关闭回调")
	}
	<-entered
	if len(m.Snapshot().Sources) != 1 {
		t.Fatal("阻塞连接被虚报为已经关闭")
	}
	if err := m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("重复退出未保留期限")
	}
	once.Do(func() { close(release) })
	final, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err := m.Stop(final); err != nil {
		t.Fatal(err)
	}
	if len(m.Snapshot().Sources) != 0 {
		t.Fatal("真实关闭引用未释放")
	}
}
