package privacy

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func TestSlowFileWriteDoesNotBlockOtherNodesButMigrationWaits(t *testing.T) {
	first := t.TempDir() + "/slow"
	second := t.TempDir() + "/fast"
	for sha256.Sum256([]byte(canonical(first)))[0] == sha256.Sum256([]byte(canonical(second)))[0] {
		second += "x"
	}
	unlock := LockFileWrite(first)
	fast := make(chan struct{})
	go func() { release := LockFileWrite(second); release(); close(fast) }()
	select {
	case <-fast:
	case <-time.After(time.Second):
		unlock()
		t.Fatal("慢节点阻塞其他文件")
	}
	migrated := make(chan struct{})
	go func() { release := LockWrite(); release(); close(migrated) }()
	select {
	case <-migrated:
		unlock()
		t.Fatal("迁移越过在途写入")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-migrated:
	case <-time.After(time.Second):
		t.Fatal("迁移屏障没有释放")
	}
}

func BenchmarkIndependentFileWriteLocks(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		path := fmt.Sprintf("/tmp/bench-node-%p", pb)
		for pb.Next() {
			release := LockFileWrite(path)
			release()
		}
	})
}
