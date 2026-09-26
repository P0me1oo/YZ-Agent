package limiter

import (
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestSpeedTracker_UpdateBuckets(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},  // 3 Mbps
		{ID: 2, UUID: "u2", SpeedLimit: 0, DeviceLimit: 0},  // unlimited
		{ID: 3, UUID: "u3", SpeedLimit: 10, DeviceLimit: 2}, // 10 Mbps
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	// User 1: 3 Mbps = 375000 bytes/sec
	b1 := st.GetLimiter("u1")
	if b1 == nil {
		t.Fatal("expected bucket for user 1")
	}
	expectedRate := float64(3 * 1000000 / 8)
	if float64(b1.Limit()) != expectedRate {
		t.Errorf("user 1 rate: got %v, want %v", float64(b1.Limit()), expectedRate)
	}
	// Burst should be max(bytesPerSec, 64KB) = max(375000, 65536) = 375000
	expectedBurst := 3 * 1000000 / 8
	if b1.Burst() != expectedBurst {
		t.Errorf("user 1 burst: got %v, want %v", b1.Burst(), expectedBurst)
	}

	// User 2: no speed limit, no bucket
	b2 := st.GetLimiter("u2")
	if b2 != nil {
		t.Fatal("expected no bucket for user 2")
	}

	// User 3: 10 Mbps = 1250000 bytes/sec
	b3 := st.GetLimiter("u3")
	if b3 == nil {
		t.Fatal("expected bucket for user 3")
	}
	expectedRate3 := float64(10 * 1000000 / 8)
	if float64(b3.Limit()) != expectedRate3 {
		t.Errorf("user 3 rate: got %v, want %v", float64(b3.Limit()), expectedRate3)
	}
	// Burst should be max(1250000, 64KB) = 1250000
	expectedBurst3 := 10 * 1000000 / 8
	if b3.Burst() != expectedBurst3 {
		t.Errorf("user 3 burst: got %v, want %v", b3.Burst(), expectedBurst3)
	}
}

func TestSpeedTracker_UpdateBuckets_PreservesExisting(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	b1 := st.GetLimiter("u1")
	if b1 == nil {
		t.Fatal("expected bucket for user 1")
	}

	// Update same user with different speed — should reuse same *rate.Limiter pointer
	users2 := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 5, DeviceLimit: 0},
	}
	l.UpdateUsers(users2)
	st.UpdateBuckets()

	b1After := st.GetLimiter("u1")
	if b1After == nil {
		t.Fatal("expected bucket for user 1 after update")
	}
	if b1 != b1After {
		t.Error("expected same rate.Limiter instance to be reused")
	}

	// Rate should be updated
	expectedRate := float64(5 * 1000000 / 8)
	if float64(b1After.Limit()) != expectedRate {
		t.Errorf("user 1 updated rate: got %v, want %v", float64(b1After.Limit()), expectedRate)
	}
}

func TestSpeedTracker_UpdateBuckets_RemovesUsers(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
		{ID: 2, UUID: "u2", SpeedLimit: 5, DeviceLimit: 0},
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	if st.GetLimiter("u1") == nil || st.GetLimiter("u2") == nil {
		t.Fatal("expected buckets for both users")
	}

	// Remove user 2
	users2 := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
	}
	l.UpdateUsers(users2)
	st.UpdateBuckets()

	if st.GetLimiter("u1") == nil {
		t.Fatal("expected bucket for user 1")
	}
	if st.GetLimiter("u2") != nil {
		t.Error("expected no bucket for removed user 2")
	}
}

// Regression: log callback must not run while UpdateBuckets holds t.mu.Lock,
// otherwise callbacks that call LimitedUserCount() self-deadlock on RWMutex.
func TestSpeedTracker_UpdateBuckets_LogCallbackMayCallLimitedUserCount(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 1}})
	st.SetLogCallback(func(msg string) {
		_ = st.LimitedUserCount()
	})

	done := make(chan struct{})
	go func() {
		st.UpdateBuckets()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: UpdateBuckets did not finish (log callback vs RWMutex)")
	}
}

// 同一用户始终使用同一个限速器对象：开始限速、调速、取消限速都原地修改，
// 已持有该对象的连接立即生效。
func TestSpeedTrackerStableLimiterFollowsPlanChanges(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1"}})
	st.UpdateBuckets()

	if st.GetLimiter("u1") != nil {
		t.Fatal("不限速用户不应返回限速器")
	}
	stable := st.GetStableLimiter("u1")
	if stable == nil || !stable.AllowN(time.Now(), 1<<20) {
		t.Fatal("固定限速器在不限速时应放行")
	}

	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 1}})
	st.UpdateBuckets()
	if st.GetStableLimiter("u1") != stable || st.GetLimiter("u1") != stable {
		t.Fatal("开始限速后应沿用同一个限速器对象")
	}
	if got := float64(stable.Limit()); got != 125000 {
		t.Fatalf("1 Mbps 应为 125000 字节每秒，实际 %v", got)
	}
	// 不限速期间积累的额度不能带入限速后的突发。
	now := time.Now()
	if !stable.AllowN(now, stable.Burst()) || stable.AllowN(now, 1024) {
		t.Fatal("切换到限速后突发额度应被限制在新的上限内")
	}
	if st.LimitedUserCount() != 1 || !st.HasLimits() {
		t.Fatal("限速用户计数错误")
	}

	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1"}})
	st.UpdateBuckets()
	if st.GetStableLimiter("u1") != stable || st.GetLimiter("u1") != nil {
		t.Fatal("取消限速后应保留同一对象并对外表现为不限速")
	}
	if !stable.AllowN(time.Now(), 1<<20) {
		t.Fatal("取消限速后已有连接应立即放开")
	}
	if st.LimitedUserCount() != 0 || st.HasLimits() {
		t.Fatal("取消限速后不应再计为限速用户")
	}
	if st.GetStableLimiter("unknown") != nil {
		t.Fatal("未知用户不应返回限速器")
	}
}
