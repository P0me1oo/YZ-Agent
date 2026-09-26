package singbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
	"golang.org/x/time/rate"
)

func TestSingBoxCapabilities(t *testing.T) {
	s := New(config.KernelConfig{Type: "sing-box"})
	caps := s.Capabilities()
	if !caps.PerUserSpeedLimit || !caps.DeviceLimit || !caps.AliveIPTracking || !caps.ForceCloseUser {
		t.Fatalf("unexpected sing-box capabilities: %+v", caps)
	}
	if caps.BuiltInTrafficStats || caps.ForceCloseConnection {
		t.Fatalf("unexpected sing-box capabilities: %+v", caps)
	}
	protocols := s.Protocols()
	found := false
	for _, protocol := range protocols {
		if protocol == "hysteria" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Protocols() missing hysteria: %v", protocols)
	}
}

type testConn struct {
	closed bool
	reads  [][]byte
	writes [][]byte
}

func (c *testConn) Read(b []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, errors.New("eof")
	}
	chunk := c.reads[0]
	c.reads = c.reads[1:]
	n := copy(b, chunk)
	return n, nil
}

func (c *testConn) Write(b []byte) (int, error) {
	cp := append([]byte(nil), b...)
	c.writes = append(c.writes, cp)
	return len(b), nil
}

func (c *testConn) Close() error                     { c.closed = true; return nil }
func (c *testConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *testConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *testConn) SetDeadline(time.Time) error      { return nil }
func (c *testConn) SetReadDeadline(time.Time) error  { return nil }
func (c *testConn) SetWriteDeadline(time.Time) error { return nil }

func testInboundContext(uuid, ip string) adapter.InboundContext {
	return adapter.InboundContext{
		User:   uuid,
		Source: singM.Socksaddr{Addr: netip.MustParseAddr(ip)},
	}
}

func TestConnTrackerRoutedConnectionTracksTrafficAndAliveIPs(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	base := &testConn{reads: [][]byte{[]byte("hello")}}

	wrapped := tracker.RoutedConnection(context.Background(), base, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)

	buf := make([]byte, 16)
	n, err := wrapped.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 5 {
		t.Fatalf("Read() bytes = %d, want 5", n)
	}
	if _, err := wrapped.Write([]byte("bye")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	traffic, aliveIPs, connCount := tracker.GetUserTraffic()
	if got := traffic[1]; got != [2]int64{5, 3} {
		t.Fatalf("traffic[1] = %v, want [5 3]", got)
	}
	if !aliveIPs[1]["1.1.1.1"] {
		t.Fatal("expected alive IP to include source address")
	}
	if connCount != 1 {
		t.Fatalf("connCount = %d, want 1", connCount)
	}

	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	_, aliveIPs, connCount = tracker.GetUserTraffic()
	if aliveIPs[1] != nil {
		t.Fatalf("aliveIPs after close = %v, want nil", aliveIPs[1])
	}
	if connCount != 0 {
		t.Fatalf("connCount after close = %d, want 0", connCount)
	}
}

func TestConnTrackerRoutedConnectionRejectsWhenDeviceLimitExceeded(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(uuid string) (int, bool) {
		if uuid != "uuid-1" {
			return 0, false
		}
		return 1, true
	})

	first := &testConn{}
	wrapped1 := tracker.RoutedConnection(context.Background(), first, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)
	if wrapped1 == first {
		t.Fatal("expected first connection to be wrapped")
	}

	second := &testConn{}
	wrapped2 := tracker.RoutedConnection(context.Background(), second, testInboundContext("uuid-1", "2.2.2.2"), nil, nil)
	if wrapped2 != second {
		t.Fatal("expected rejected connection to be returned unwrapped")
	}
	if !second.closed {
		t.Fatal("expected rejected connection to be closed")
	}
}

func TestConnTrackerPrivateRelaySourceDoesNotUseDeviceSlot(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(string) (int, bool) { return 1, true })
	tracker.UpdateGlobalDevices(map[int][]string{1: {"8.8.8.8"}})

	private := &testConn{}
	wrapped := tracker.RoutedConnection(context.Background(), private,
		testInboundContext("uuid-1", "10.0.0.2"), nil, nil)
	if wrapped == private || private.closed {
		t.Fatal("内网中转来源不应占用设备名额或被设备上限拒绝")
	}
	// 内核仍登记该来源供在线人数统计，设备上报由 tracker 按计数规则过滤。
	_, ips, connections := tracker.GetUserTraffic()
	if !ips[1]["10.0.0.2"] || connections != 1 {
		t.Fatalf("内网连接应登记来源和连接数，来源=%v，连接数=%d", ips, connections)
	}
	_ = wrapped.Close()
}

func TestConnTrackerExcludedSourceDoesNotUseDeviceSlot(t *testing.T) {
	deviceip.SetExcluded([]string{"203.0.114.0/24"})
	t.Cleanup(func() { deviceip.SetExcluded(nil) })

	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(string) (int, bool) { return 1, true })
	tracker.UpdateGlobalDevices(map[int][]string{1: {"8.8.8.8"}})

	relay := &testConn{}
	wrapped := tracker.RoutedConnection(context.Background(), relay,
		testInboundContext("uuid-1", "203.0.114.7"), nil, nil)
	if wrapped == relay || relay.closed {
		t.Fatal("名单内的转发来源不应被设备上限拒绝")
	}
	blocked := &testConn{}
	if tracker.RoutedConnection(context.Background(), blocked,
		testInboundContext("uuid-1", "1.1.1.1"), nil, nil) != blocked || !blocked.closed {
		t.Fatal("名额已满时新的公网来源仍应被拒绝")
	}

	// 连接存续期间移出名单，该来源立即按公网来源计数。
	deviceip.SetExcluded(nil)
	tracker.UpdateGlobalDevices(nil)
	late := &testConn{}
	if tracker.RoutedConnection(context.Background(), late,
		testInboundContext("uuid-1", "1.1.1.1"), nil, nil) != late || !late.closed {
		t.Fatal("移出名单后原转发来源应占用名额")
	}
	_ = wrapped.Close()
	_, ips, connections := tracker.GetUserTraffic()
	if len(ips) != 0 || connections != 0 {
		t.Fatalf("关闭后应清理登记，来源=%v，连接数=%d", ips, connections)
	}
}

func TestConnTrackerCheckDeviceGateMergesFreshGlobalDevices(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	us := tracker.users[1]
	us.addConn("1.1.1.1")
	tracker.UpdateGlobalDevices(map[int][]string{1: {"9.9.9.9"}})

	if !tracker.checkDeviceGate(us, 1, "2.2.2.2", 2) {
		t.Fatal("new source must be rejected after local and global slots are occupied")
	}
	if !tracker.checkDeviceGate(us, 1, "99.99.99.99", 2) {
		t.Fatal("new source must be rejected regardless of its address")
	}
	if tracker.checkDeviceGate(us, 1, "1.1.1.1", 2) {
		t.Fatal("existing local IP should still be allowed")
	}
	if tracker.checkDeviceGate(us, 1, "9.9.9.9", 2) {
		t.Fatal("existing global IP should still be allowed")
	}
}

func TestConnTrackerConcurrentAdmissionRespectsDeviceLimit(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(string) (int, bool) { return 3, true })
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := make([]net.Conn, 0, 3)
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			base := &testConn{}
			conn := tracker.RoutedConnection(context.Background(), base,
				testInboundContext("uuid-1", fmt.Sprintf("8.8.8.%d", i)), nil, nil)
			if conn != base {
				mu.Lock()
				accepted = append(accepted, conn)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 3 {
		t.Fatalf("accepted %d distinct sources, want 3", len(accepted))
	}
	for _, conn := range accepted {
		_ = conn.Close()
	}
}

func TestConnTrackerCloseByIDClosesTrackedConnection(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	base := &testConn{}
	wrapped := tracker.RoutedConnection(context.Background(), base, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)
	tracked, ok := wrapped.(*trackedConn)
	if !ok {
		t.Fatalf("wrapped type = %T, want *trackedConn", wrapped)
	}
	if !tracker.CloseByID(tracked.connID) {
		t.Fatal("CloseByID() = false, want true")
	}
	if !base.closed {
		t.Fatal("expected underlying connection to be closed")
	}
}

func TestConnTrackerRateLimitHonorsContextCancellation(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetSpeedLimitFunc(func(uuid string) *rate.Limiter {
		if uuid != "uuid-1" {
			return nil
		}
		return rate.NewLimiter(rate.Limit(1), 1)
	})

	readCtx, cancelRead := context.WithCancel(context.Background())
	readBase := &testConn{reads: [][]byte{[]byte("a"), []byte("a")}}
	readTracked := tracker.RoutedConnection(readCtx, readBase, testInboundContext("uuid-1", "1.1.1.1"), nil, nil).(*trackedConn)
	buf := make([]byte, 8)
	if n, err := readTracked.Read(buf); err != nil || n != 1 {
		t.Fatalf("first Read() = (%d, %v), want (1, nil)", n, err)
	}
	cancelRead()
	if n, err := readTracked.Read(buf); !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("second Read() = (%d, %v), want (1, context canceled)", n, err)
	}

	writeCtx, cancelWrite := context.WithCancel(context.Background())
	writeBase := &testConn{}
	writeTracked := tracker.RoutedConnection(writeCtx, writeBase, testInboundContext("uuid-1", "1.1.1.2"), nil, nil).(*trackedConn)
	if n, err := writeTracked.Write([]byte("a")); err != nil || n != 1 {
		t.Fatalf("first Write() = (%d, %v), want (1, nil)", n, err)
	}
	cancelWrite()
	if n, err := writeTracked.Write([]byte("a")); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("second Write() = (%d, %v), want (0, context canceled)", n, err)
	}
}

// 设备上限为 1 时，同一 IPv6 /64 网段轮换出的新地址属于已在线的设备，不应被拒绝。
func TestConnTrackerIPv6SamePrefixUsesOneDeviceSlot(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(string) (int, bool) { return 1, true })

	first := tracker.RoutedConnection(context.Background(), &testConn{}, testInboundContext("uuid-1", "2400:cb00:1:2::10"), nil, nil)
	rotated := &testConn{}
	if tracker.RoutedConnection(context.Background(), rotated, testInboundContext("uuid-1", "2400:cb00:1:2::abcd"), nil, nil) == rotated || rotated.closed {
		t.Fatal("同一网段的临时地址应视为同一设备")
	}
	other := &testConn{}
	if tracker.RoutedConnection(context.Background(), other, testInboundContext("uuid-1", "2400:cb00:1:3::1"), nil, nil) != other || !other.closed {
		t.Fatal("其他网段的地址仍应占用新名额")
	}
	// 面板快照里的网段地址与本机来源按同一口径比较。
	tracker.UpdateGlobalDevices(map[int][]string{1: {"2400:cb00:9:9::"}})
	us := tracker.users[1]
	if tracker.checkDeviceGate(us, 1, "2400:cb00:9:9::77", 2) {
		t.Fatal("其他节点已登记的网段应视为已在线设备")
	}
	_ = first.Close()
}
