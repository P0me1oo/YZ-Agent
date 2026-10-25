package balancer

import (
	"sync"
	"testing"
)

func TestRoundRobinPriorityAndRecovery(t *testing.T) {
	targets := []Target{{ID: 1, Address: "127.0.0.1:1", Priority: 1, Online: true}, {ID: 2, Address: "127.0.0.1:2", Priority: 1, Online: true}, {ID: 3, Address: "127.0.0.1:3", Priority: 2, Online: true}}
	s, err := NewSelector(targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []int{1, 2, 1, 2} {
		got, ok := s.Pick(nil)
		if !ok || got.ID != expected {
			t.Fatalf("got %d, expected %d", got.ID, expected)
		}
	}
	targets[0].Online, targets[1].Online = false, false
	if err := s.Update(targets); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Pick(nil); !ok || got.ID != 3 {
		t.Fatal("备用成员没有接管")
	}
	targets[1].Online = true
	if err := s.Update(targets); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Pick(nil); !ok || got.ID != 2 {
		t.Fatal("恢复后没有回到高优先级")
	}
	if _, ok := s.Pick(map[int]bool{2: true, 3: true}); ok {
		t.Fatal("不应回退到离线成员")
	}
}

func TestRoundRobinConcurrentAndInvalidUpdate(t *testing.T) {
	s, err := NewSelector([]Target{{1, "127.0.0.1:1", 1, true}, {2, "127.0.0.1:2", 1, true}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	counts := make(map[int]int)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target, ok := s.Pick(nil)
			if !ok {
				t.Error("没有可用成员")
				return
			}
			mu.Lock()
			counts[target.ID]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[1] != 50 || counts[2] != 50 {
		t.Fatal(counts)
	}
	if s.Update([]Target{{1, "invalid", 1, true}}) == nil {
		t.Fatal("接受了无效配置")
	}
	if _, ok := s.Pick(nil); !ok {
		t.Fatal("失败更新覆盖了有效配置")
	}
}
