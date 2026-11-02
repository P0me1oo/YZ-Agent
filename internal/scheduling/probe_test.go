package scheduling

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFailureRecoveryAndTargetChange(t *testing.T) {
	m := New()
	target := Target{NodeID: 7, Address: "127.0.0.1", Port: 12345, Token: strings.Repeat("a", 64)}
	m.Update([]Target{target})
	m.record(target, true, 5, 1)
	m.record(target, false, 0, 2)
	if !m.Snapshot()[0].Healthy {
		t.Fatal("a single failure removed the target")
	}
	m.record(target, false, 0, 2)
	if !m.Snapshot()[0].Healthy {
		t.Fatal("duplicate samples must not count twice")
	}
	m.record(target, false, 0, 3)
	if m.Snapshot()[0].Healthy {
		t.Fatal("two failures should remove the target")
	}
	m.record(target, true, 3, 4)
	if m.Snapshot()[0].Healthy {
		t.Fatal("a single success must not restore the target")
	}
	m.record(target, true, 4, 5)
	snapshot := m.Snapshot()
	if !snapshot[0].Healthy || snapshot[0].RTTMs != 4 {
		t.Fatal(snapshot)
	}
	snapshot[0].Healthy = false
	if !m.Snapshot()[0].Healthy {
		t.Fatal("snapshot leaked mutable state")
	}
	replacement := target
	replacement.Port++
	replacement.Token = strings.Repeat("b", 64)
	m.Update([]Target{replacement})
	m.record(target, true, 1, 5)
	if len(m.Snapshot()) != 0 {
		t.Fatal("old address result survived replacement")
	}
	m.Update(nil)
	m.record(replacement, true, 1, 6)
	if len(m.Snapshot()) != 0 {
		t.Fatal("deleted target retained measurements")
	}
}

func TestRealTCPProbeAndCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	m := New()
	m.Update([]Target{{NodeID: 1, Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Token: strings.Repeat("c", 64)}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(m.Snapshot()) != 1 || !m.Snapshot()[0].Healthy {
		cancel()
		t.Fatal("TCP target was not measured")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}
