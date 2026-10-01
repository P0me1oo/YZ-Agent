package kernel

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestRelayAccessRevocationClosesOnlyRevokedConnections(t *testing.T) {
	var access RelayAccess
	var revoked, retained atomic.Int32
	access.Replace(map[string]bool{"old": true, "kept": true})
	if !access.Register("tcp", "old", func() { revoked.Add(1); access.Remove("tcp") }) ||
		!access.Register("udp", "kept", func() { retained.Add(1); access.Remove("udp") }) {
		t.Fatal("正常线路被拒绝")
	}
	access.Replace(map[string]bool{"kept": true})
	access.Replace(map[string]bool{"kept": true})
	if revoked.Load() != 1 || retained.Load() != 0 {
		t.Fatal("撤权影响了其他线路，或重复关闭")
	}
	if access.Allows("old") || access.Register("late", "old", func() {}) {
		t.Fatal("撤权后的请求被放行")
	}
	access.Replace(map[string]bool{})
	if retained.Load() != 1 {
		t.Fatal("清空权限没有关闭剩余连接")
	}
}

func TestRelayAccessConcurrentRegistrationCannotEscapeRevocation(t *testing.T) {
	for range 100 {
		var access RelayAccess
		var closed atomic.Bool
		access.Replace(map[string]bool{"route": true})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !access.Register("connection", "route", func() { closed.Store(true) }) {
				closed.Store(true)
			}
		}()
		access.Replace(map[string]bool{})
		wg.Wait()
		if !closed.Load() {
			t.Fatal("并发登记绕过了撤权")
		}
	}
}
