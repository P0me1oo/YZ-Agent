package xray

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/transport"
)

func newTestDispatcher() *LimitDispatcher {
	return &LimitDispatcher{
		limitedIPs: make(map[string]map[string]int),
	}
}

type nopReader struct{}

func (nopReader) ReadMultiBuffer() (buf.MultiBuffer, error) { return nil, nil }

func TestLimitDispatcher_DeviceLimitCheck(t *testing.T) {
	ld := newTestDispatcher()

	users := []model.UserSpec{
		{ID: 1, UUID: "uuid-1", DeviceLimit: 2, SpeedLimit: 0},
		{ID: 2, UUID: "uuid-2", DeviceLimit: 0, SpeedLimit: 10},
	}

	emailToUID := make(map[string]int)
	deviceLimits := make(map[string]int)
	speedLimits := make(map[string]int)
	for _, u := range users {
		email := userEmail(u.ID)
		emailToUID[email] = u.ID
		if u.DeviceLimit > 0 {
			deviceLimits[email] = u.DeviceLimit
		}
		if u.SpeedLimit > 0 {
			speedLimits[email] = u.SpeedLimit
		}
	}
	ld.UpdateLimits(emailToUID, deviceLimits, speedLimits)

	email1 := userEmail(1)

	// First IP should be allowed
	if ld.checkDeviceLimit(email1, "1.1.1.1", true) {
		t.Error("first IP should be allowed")
	}

	// Second IP should be allowed (limit=2)
	if ld.checkDeviceLimit(email1, "2.2.2.2", true) {
		t.Error("second IP should be allowed")
	}

	// Third unique IP should be rejected
	if !ld.checkDeviceLimit(email1, "3.3.3.3", true) {
		t.Error("third IP should be rejected (limit=2)")
	}

	// Same IP as first should be allowed (already connected)
	if ld.checkDeviceLimit(email1, "1.1.1.1", true) {
		t.Error("same IP should always be allowed")
	}

	// User 2 has no device limit — should always be allowed
	email2 := userEmail(2)
	for i := 0; i < 10; i++ {
		ip := "10.0.0." + string(rune('0'+i))
		if ld.checkDeviceLimit(email2, ip, true) {
			t.Errorf("user with no device limit should always be allowed (ip=%s)", ip)
		}
	}
}

func TestLimitDispatcherUsesFreshGlobalDevices(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 2}, nil)
	ld.UpdateGlobalDevices(map[int][]string{15: {"10.0.0.1", "8.8.8.8"}}, time.Now())
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("private global source must not occupy a slot")
	}
	ld.delConn(email, "1.1.1.1")
	ld.UpdateGlobalDevices(map[int][]string{15: {"8.8.8.8", "9.9.9.9"}}, time.Now())
	if !ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("new source should be rejected when global slots are full")
	}
	if ld.checkDeviceLimit(email, "10.0.0.1", true) {
		t.Fatal("existing private source should remain allowed")
	}
	ld.delConn(email, "10.0.0.1")
	ld.UpdateGlobalDevices(map[int][]string{15: {"8.8.8.8"}}, time.Now())
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("new source should be allowed after a slot is released")
	}
	ld.delConn(email, "1.1.1.1")
	ld.UpdateGlobalDevices(map[int][]string{15: {"10.0.0.1", "8.8.8.8"}}, time.Now().Add(-3*time.Minute))
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("stale global snapshot should fall back to local state")
	}
	ld.delConn(email, "1.1.1.1")
}

func TestLimitDispatcherPrivateRelaySourceDoesNotUseDeviceSlot(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 1}, nil)
	ld.UpdateGlobalDevices(map[int][]string{15: {"8.8.8.8"}}, time.Now())
	if ld.checkDeviceLimit(email, "10.0.0.2", true) {
		t.Fatal("内网出口不应被设备上限拒绝")
	}
	ld.delConn(email, "10.0.0.2")
	ips, _ := ld.GetConnectionState()
	if len(ips) != 0 {
		t.Fatalf("内网出口不应上报为设备: %v", ips)
	}
	if !ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("不同公网来源仍应受上限约束")
	}
}

func TestXrayForwardsAndClearsGlobalDevices(t *testing.T) {
	x := New(config.KernelConfig{Type: "xray"})
	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 1}, nil)
	x.limitDispatcher = ld
	x.UpdateGlobalDevices(map[int][]string{15: {"8.8.8.8"}})
	if !ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("global state was not forwarded")
	}
	x.ClearGlobalDevices()
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("disconnected panel state should not keep rejecting new sources")
	}
	ld.delConn(email, "1.1.1.1")
}

func TestLimitDispatcherCountsUDPSources(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 1}, nil)
	if ld.checkDeviceLimit(email, "8.8.8.8", false) {
		t.Fatal("first UDP source should be allowed")
	}
	if !ld.checkDeviceLimit(email, "8.8.8.9", false) {
		t.Fatal("second UDP source should be rejected")
	}
	ld.delConn(email, "8.8.8.8")
	if ld.checkDeviceLimit(email, "8.8.8.9", false) {
		t.Fatal("released UDP source should free its slot")
	}
	ld.delConn(email, "8.8.8.9")
}

func TestLimitDispatcherConcurrentAdmissionRespectsDeviceLimit(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 3}, nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := make([]string, 0, 3)
	for i := 1; i <= 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := fmt.Sprintf("8.8.8.%d", i)
			if !ld.checkDeviceLimit(email, ip, true) {
				mu.Lock()
				accepted = append(accepted, ip)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 3 {
		t.Fatalf("accepted %d distinct sources, want 3", len(accepted))
	}
	for _, ip := range accepted {
		ld.delConn(email, ip)
	}
}

func TestLimitDispatcher_DelConn(t *testing.T) {
	ld := newTestDispatcher()

	email := userEmail(1)
	deviceLimits := map[string]int{email: 2}
	ld.UpdateLimits(map[string]int{email: 1}, deviceLimits, nil)

	// Add 2 IPs
	ld.checkDeviceLimit(email, "1.1.1.1", true)
	ld.checkDeviceLimit(email, "2.2.2.2", true)

	// Third should be rejected
	if !ld.checkDeviceLimit(email, "3.3.3.3", true) {
		t.Error("third IP should be rejected")
	}

	// Remove first IP
	ld.delConn(email, "1.1.1.1")

	// Now third IP should be allowed
	if ld.checkDeviceLimit(email, "3.3.3.3", true) {
		t.Error("after deleting one IP, new IP should be allowed")
	}
}

func TestLimitDispatcher_GetConnectionState(t *testing.T) {
	ld := newTestDispatcher()

	email1 := userEmail(1)
	email2 := userEmail(2)
	ld.UpdateLimits(map[string]int{email1: 1, email2: 2}, nil, nil)

	ic1 := &ipCounter{}
	r1 := &atomic.Int64{}
	r1.Store(1)
	ic1.ips.Store("1.1.1.1", r1)
	r2 := &atomic.Int64{}
	r2.Store(1)
	ic1.ips.Store("2.2.2.2", r2)
	ld.unlimitedIPs.Store(email1, ic1)

	ic2 := &ipCounter{}
	r3 := &atomic.Int64{}
	r3.Store(1)
	ic2.ips.Store("3.3.3.3", r3)
	ld.unlimitedIPs.Store(email2, ic2)

	ld.connCount.Store(5)

	aliveIPs, connCount := ld.GetConnectionState()

	if connCount != 5 {
		t.Errorf("expected connCount=5, got %d", connCount)
	}
	if len(aliveIPs[1]) != 2 {
		t.Errorf("user 1 IPs: got %d, want 2", len(aliveIPs[1]))
	}
	if len(aliveIPs[2]) != 1 {
		t.Errorf("user 2 IPs: got %d, want 1", len(aliveIPs[2]))
	}
}

func TestLimitDispatcher_ResetConns(t *testing.T) {
	ld := newTestDispatcher()

	ld.mu.Lock()
	ld.limitedIPs["user@1"] = map[string]int{"1.1.1.1": 1}
	ld.mu.Unlock()
	ld.connCount.Store(3)

	ld.ResetConns()

	ld.mu.RLock()
	ipCount := len(ld.limitedIPs)
	ld.mu.RUnlock()
	if ipCount != 0 {
		t.Error("limitedIPs should be empty after reset")
	}

	if ld.connCount.Load() != 0 {
		t.Error("connCount should be 0 after reset")
	}
}

func TestLimitDispatcher_UnlimitedUserFastPath(t *testing.T) {
	ld := newTestDispatcher()

	email := userEmail(1)
	// No device limit set for this user
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)

	// Should use fast path (sync.Map), no lock needed
	for i := 0; i < 100; i++ {
		ip := "8.8.8." + string(rune('0'+i%10))
		if ld.checkDeviceLimit(email, ip, true) {
			t.Errorf("unlimited user should always be allowed (ip=%s)", ip)
		}
	}

	// Verify IPs are tracked in unlimitedIPs
	v, ok := ld.unlimitedIPs.Load(email)
	if !ok {
		t.Error("unlimited user should have entry in unlimitedIPs")
	}
	ic := v.(*ipCounter)
	ips := ic.aliveIPs()
	if len(ips) == 0 {
		t.Error("should have tracked some IPs")
	}
}

func TestLimitDispatcher_TrackLinkPreservesReader(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)

	origReader := nopReader{}
	origWriter := &closeTrackingWriter{Writer: buf.Discard, onClose: func() {}}
	link := &transport.Link{Reader: origReader, Writer: origWriter}

	ld.trackLink(link, email, "1.1.1.1", 0, true)

	if link.Reader != origReader {
		t.Fatal("trackLink must not replace link.Reader")
	}
	if link.Writer == origWriter {
		t.Fatal("trackLink should wrap link.Writer for lifecycle callbacks")
	}
}

func TestLimitDispatcher_CloseTrackingWriterReleasesConn(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, map[string]int{email: 1}, nil)
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("first connection should be allowed")
	}

	link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
	ld.trackLink(link, email, "1.1.1.1", 0, true)

	if got := ld.connCount.Load(); got != 1 {
		t.Fatalf("expected connCount=1 after tracking, got %d", got)
	}
	cw, ok := link.Writer.(*closeTrackingWriter)
	if !ok {
		t.Fatal("expected closeTrackingWriter wrapper")
	}
	if err := cw.Close(); err != nil {
		t.Fatalf("closeTrackingWriter.Close() error = %v", err)
	}
	if got := ld.connCount.Load(); got != 0 {
		t.Fatalf("expected connCount=0 after close, got %d", got)
	}
	if ld.checkDeviceLimit(email, "2.2.2.2", true) {
		t.Fatal("device slot should be released after writer close")
	}
}

// stubConnLimiter 用固定配置模拟 limiter，记录被拒次数供断言。
type stubConnLimiter struct {
	maxConn   int
	allowRate bool
	reports   []string
}

func (s *stubConnLimiter) MaxConnByUserID(int) (int, bool) {
	if s.maxConn <= 0 {
		return 0, false
	}
	return s.maxConn, true
}

func (s *stubConnLimiter) AllowNewConn(int) bool { return s.allowRate }

func (s *stubConnLimiter) ReportLimited(_ int, kind string, _, _ int) {
	s.reports = append(s.reports, kind)
}

func TestLimitDispatcher_ConnGateRejectsOverConcurrency(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)

	stub := &stubConnLimiter{maxConn: 2, allowRate: true}
	ld.SetConnLimiter(stub)

	// 两条连接占满并发上限。
	for i := 0; i < 2; i++ {
		uid, _, _, _, reject := ld.checkConnGate(email)
		if reject {
			t.Fatalf("第 %d 条连接应在并发上限内", i+1)
		}
		link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
		ld.trackLink(link, email, "1.1.1.1", uid, true)
	}

	_, kind, limit, observed, reject := ld.checkConnGate(email)
	if !reject || kind != model.ConnLimitKindConcurrent {
		t.Fatalf("第 3 条连接应因并发超限被拒，实际 reject=%v kind=%q", reject, kind)
	}
	if limit != 2 || observed != 2 {
		t.Fatalf("上报值 = (limit=%d, observed=%d)，期望 (2, 2)", limit, observed)
	}
	if len(stub.reports) != 1 || stub.reports[0] != model.ConnLimitKindConcurrent {
		t.Fatalf("超限上报 = %#v，期望一条并发超限", stub.reports)
	}
}

func TestLimitDispatcher_ConnGateReleasesOnClose(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)
	ld.SetConnLimiter(&stubConnLimiter{maxConn: 1, allowRate: true})

	uid, _, _, _, reject := ld.checkConnGate(email)
	if reject {
		t.Fatal("第一条连接应被放行")
	}
	link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
	ld.trackLink(link, email, "1.1.1.1", uid, true)

	if _, _, _, _, reject := ld.checkConnGate(email); !reject {
		t.Fatal("并发已占满，第二条应被拒")
	}

	if err := link.Writer.(*closeTrackingWriter).Close(); err != nil {
		t.Fatalf("closeTrackingWriter.Close() error = %v", err)
	}
	if _, _, _, _, reject := ld.checkConnGate(email); reject {
		t.Fatal("连接关闭后并发名额应释放")
	}
}

func TestLimitDispatcher_ConnGateRejectsOverRate(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)
	ld.SetConnLimiter(&stubConnLimiter{allowRate: false})

	_, kind, _, _, reject := ld.checkConnGate(email)
	if !reject || kind != model.ConnLimitKindRate {
		t.Fatalf("速率超限应被拒，实际 reject=%v kind=%q", reject, kind)
	}
	if got := ld.userConnCounter(1).Load(); got != 0 {
		t.Fatalf("速率拒绝后仍占用 %d 个名额", got)
	}
}

func TestLimitDispatcher_ConnGateSkippedWithoutLimiter(t *testing.T) {
	ld := newTestDispatcher()
	email := userEmail(1)
	ld.UpdateLimits(map[string]int{email: 1}, nil, nil)

	uid, _, _, _, reject := ld.checkConnGate(email)
	if reject {
		t.Fatal("没装 connLimiter 时不应拒绝连接")
	}
	if uid != 0 {
		t.Fatalf("没装 connLimiter 时不应解析 userID，实际 %d", uid)
	}
}

func TestLimitDispatcherExcludedSourceDoesNotUseDeviceSlot(t *testing.T) {
	deviceip.SetExcluded([]string{"203.0.114.7"})
	t.Cleanup(func() { deviceip.SetExcluded(nil) })

	ld := newTestDispatcher()
	email := userEmail(15)
	ld.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 1}, nil)
	ld.UpdateGlobalDevices(map[int][]string{15: {"8.8.8.8", "203.0.114.7"}}, time.Now())
	if ld.checkDeviceLimit(email, "203.0.114.7", true) {
		t.Fatal("名单内的转发来源不应被设备上限拒绝")
	}
	ips, _ := ld.GetConnectionState()
	if !ips[15]["203.0.114.7"] {
		t.Fatalf("名单内来源仍应登记，供在线人数统计: %v", ips)
	}
	if !ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("全局快照已占满名额时新的公网来源应被拒绝")
	}

	// 连接存续期间移出名单，原转发来源立即参与计数。
	deviceip.SetExcluded(nil)
	ld.UpdateGlobalDevices(map[int][]string{15: {}}, time.Now())
	if !ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("移出名单后原转发来源应占用名额")
	}
	ld.delConn(email, "203.0.114.7")
	if ld.checkDeviceLimit(email, "1.1.1.1", true) {
		t.Fatal("转发连接关闭后应释放名额")
	}
	ld.delConn(email, "1.1.1.1")
	if ips, _ := ld.GetConnectionState(); len(ips) != 0 {
		t.Fatalf("连接全部关闭后不应残留登记: %v", ips)
	}
}
