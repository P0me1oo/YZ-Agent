package directwg

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"golang.org/x/time/rate"
)

func stateUser(t *testing.T) model.UserSpec {
	t.Helper()
	k, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return model.UserSpec{ID: 1, UUID: "dedicated-test-user", DeviceLimit: 1, WireGuard: &panel.WireGuardPeer{PublicKey: base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), Address: []string{"10.0.0.2/32"}}}
}
func TestIPAdmissionSharesQuotaAndExclusions(t *testing.T) {
	s := newState()
	u := stateUser(t)
	s.replaceUsers([]model.UserSpec{u})
	now := time.Now()
	key := u.WireGuard.PublicKey
	s.global = map[int][]string{1: {"8.8.8.8"}}
	s.globalAt = now
	if s.admit(key, netip.MustParseAddr("1.1.1.1"), true, now) {
		t.Fatal("跨协议设备额度被绕过")
	}
	if !s.admit(key, netip.MustParseAddr("8.8.8.8"), true, now) {
		t.Fatal("已有来源被重复计数")
	}
	s.filter.SetExcluded([]string{"1.1.1.1"})
	if !s.admit(key, netip.MustParseAddr("1.1.1.1"), true, now) {
		t.Fatal("排除来源被拒绝")
	}
	if s.admit(key, netip.MustParseAddr("9.9.9.9"), false, now) {
		t.Fatal("空保活建立了未准入的来源")
	}
	if !s.admit(key, netip.MustParseAddr("9.9.9.9"), true, now.Add(sourceTTL+time.Second)) {
		t.Fatal("失联来源没有释放名额")
	}
}
func TestIPv6SourcesMergeBy64AndAdmissionIsAtomic(t *testing.T) {
	s := newState()
	u := stateUser(t)
	s.replaceUsers([]model.UserSpec{u})
	now := time.Now()
	key := u.WireGuard.PublicKey
	for _, ip := range []string{"2606:4700:1:2::1", "2606:4700:1:2::2"} {
		if !s.admit(key, netip.MustParseAddr(ip), true, now) {
			t.Fatal("同一 IPv6 网段被重复计数")
		}
	}
	if s.admit(key, netip.MustParseAddr("2606:4700:1:3::1"), true, now) {
		t.Fatal("不同 IPv6 网段绕过名额")
	}
	s.replaceUsers(nil)
	s.replaceUsers([]model.UserSpec{u})
	var accepted atomic.Int32
	var work sync.WaitGroup
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "9.9.9.9"} {
		work.Add(1)
		go func(ip string) {
			defer work.Done()
			if s.admit(key, netip.MustParseAddr(ip), true, now) {
				accepted.Add(1)
			}
		}(ip)
	}
	work.Wait()
	if accepted.Load() != 1 {
		t.Fatal("并发准入超额")
	}
}
func TestExpiryRotationAndTotalsSurviveRemoval(t *testing.T) {
	s := newState()
	u := stateUser(t)
	s.replaceUsers([]model.UserSpec{u})
	old := s.users[1]
	if !s.transfer(1, 0, 123, old) {
		t.Fatal("有效用户没有记录流量")
	}
	changed := stateUser(t)
	s.replaceUsers([]model.UserSpec{changed})
	if s.transfer(1, 0, 100, old) {
		t.Fatal("旧密钥排队中的报文在轮换后仍被接受")
	}
	if s.admit(u.WireGuard.PublicKey, netip.MustParseAddr("8.8.8.8"), true, time.Now()) {
		t.Fatal("旧公钥仍然有效")
	}
	changed.WireGuard.ExpiresAt = time.Now().Unix() - 1
	s.replaceUsers([]model.UserSpec{changed})
	if s.transfer(1, 1, 321, nil) {
		t.Fatal("到期后仍然传输")
	}
	s.replaceUsers(nil)
	totals, _, _ := s.snapshot()
	if totals[1][0] != 123 || totals[1][1] != 0 {
		t.Fatal("用户移除后累计流量被清空或重复结算")
	}
}
func TestRateLimitAndLiveLimitChange(t *testing.T) {
	s := newState()
	u := stateUser(t)
	s.replaceUsers([]model.UserSpec{u})
	limiter := rate.NewLimiter(1000, 100)
	s.speed = func(string) *rate.Limiter { return limiter }
	started := time.Now()
	if !s.wait(context.Background(), 1, 500, s.users[1]) {
		t.Fatal("限速流量未通过")
	}
	if time.Since(started) < 300*time.Millisecond {
		t.Fatal("限速未生效")
	}
	limiter.SetLimit(rate.Inf)
	started = time.Now()
	if !s.wait(context.Background(), 1, 500, s.users[1]) || time.Since(started) > 200*time.Millisecond {
		t.Fatal("解除限速未即时生效")
	}
}
