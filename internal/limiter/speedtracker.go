package limiter

import (
	"sync"
	"sync/atomic"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"golang.org/x/time/rate"
)

// SpeedTrackerLogCallback is called when bucket updates occur.
type SpeedTrackerLogCallback func(msg string)

// 不限速用户的固定限速器使用极高的有限速率，而不是 rate.Inf：
// rate.Inf 在切回有限速率时会让令牌数变成无效值，限速随之失效。
const (
	unlimitedBytesPerSec = 1 << 50
	unlimitedBurst       = 1 << 30
)

// SpeedTracker manages per-user token-bucket rate limiters.
// It does NOT wrap connections itself — instead, ConnTracker consults it
// via GetLimiter to embed rate limiting in the same tracked connection
// wrapper that does byte counting.
//
// 每个用户最多一个限速器对象，同一用户的所有连接共用额度。套餐调整时原地修改
// 该对象，不重建，已持有它的连接立即按新速率执行。
type SpeedTracker struct {
	limiter *Limiter
	mu      sync.RWMutex
	buckets map[int]*rate.Limiter // userID → shared rate limiter
	uuidMap map[string]int        // UUID → userID

	// Fast-path: when no users have a speed limit, GetLimiter returns nil
	// immediately without any map lookup.
	hasLimits atomic.Bool

	// Optional callback for logging
	logFunc SpeedTrackerLogCallback
}

// NewSpeedTracker creates a bucket manager for per-user bandwidth throttling.
func NewSpeedTracker(l *Limiter) *SpeedTracker {
	return &SpeedTracker{
		limiter: l,
		buckets: make(map[int]*rate.Limiter),
		uuidMap: make(map[string]int),
	}
}

// SetLogCallback sets the logging callback.
func (t *SpeedTracker) SetLogCallback(f SpeedTrackerLogCallback) {
	t.logFunc = f
}

// speedSettings 把套餐速率（Mbps）换算成令牌桶的速率和突发额度。
func speedSettings(speedLimit int) (rate.Limit, int) {
	if speedLimit <= 0 {
		return rate.Limit(unlimitedBytesPerSec), unlimitedBurst
	}
	bytesPerSec := speedLimit * 1_000_000 / 8
	burst := bytesPerSec
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	if cap4s := bytesPerSec * 4; cap4s > 64*1024 && burst > cap4s {
		burst = cap4s
	}
	return rate.Limit(bytesPerSec), burst
}

// UpdateBuckets updates the UUID→userID mapping and syncs existing limiters.
func (t *SpeedTracker) UpdateBuckets() {
	currentUsers := make([]model.UserSpec, 0, 32)
	t.limiter.mu.RLock()
	for _, u := range t.limiter.users {
		currentUsers = append(currentUsers, u)
	}
	t.limiter.mu.RUnlock()

	func() {
		t.mu.Lock()
		defer t.mu.Unlock()

		newUUIDMap := make(map[string]int, len(currentUsers))
		activeIDs := make(map[int]struct{}, len(currentUsers))
		hasLimits := false

		for _, user := range currentUsers {
			activeIDs[user.ID] = struct{}{}
			if user.UUID != "" {
				newUUIDMap[user.UUID] = user.ID
			}
			if user.SpeedLimit > 0 {
				hasLimits = true
			}

			// 已有限速器原地调速；取消限速时保留对象并放开速率，持有它的连接随即不再受限。
			if lim, ok := t.buckets[user.ID]; ok {
				limit, burst := speedSettings(user.SpeedLimit)
				lim.SetLimit(limit)
				lim.SetBurst(burst)
			}
		}

		// Clean up buckets for removed users
		for id := range t.buckets {
			if _, ok := activeIDs[id]; !ok {
				delete(t.buckets, id)
			}
		}

		t.uuidMap = newUUIDMap
		t.hasLimits.Store(hasLimits)
	}()

	if t.logFunc != nil {
		t.logFunc("buckets updated")
	}
}

// GetLimiter returns the rate limiter for the given user UUID, or nil if
// no limit applies. Creates limiter on-demand if not exists.
// Thread-safe.
func (t *SpeedTracker) GetLimiter(user string) *rate.Limiter {
	lim, limited := t.limiterFor(user)
	if !limited {
		return nil
	}
	return lim
}

// GetStableLimiter 为已知用户返回固定的限速器对象，不限速的用户也返回（速率放开）。
// 供在连接建立时固定持有限速器的内核使用；未知用户返回 nil。
func (t *SpeedTracker) GetStableLimiter(user string) *rate.Limiter {
	lim, _ := t.limiterFor(user)
	return lim
}

// limiterFor 返回用户的限速器（按需创建）以及该用户当前是否限速。
func (t *SpeedTracker) limiterFor(user string) (*rate.Limiter, bool) {
	// 查用户、建桶与更新桶使用同一把锁，避免首次连接把过期的不限速设置写回。
	t.mu.Lock()
	defer t.mu.Unlock()
	uid, exists := t.uuidMap[user]
	if !exists {
		return nil, false
	}
	t.limiter.mu.RLock()
	u, userExists := t.limiter.users[uid]
	t.limiter.mu.RUnlock()
	if !userExists {
		return nil, false
	}
	limited := u.SpeedLimit > 0
	if lim := t.buckets[uid]; lim != nil {
		return lim, limited
	}
	limit, burst := speedSettings(u.SpeedLimit)
	created := rate.NewLimiter(limit, burst)
	t.buckets[uid] = created
	return created, limited
}

// HasLimits returns true if any user currently has a speed limit configured.
func (t *SpeedTracker) HasLimits() bool {
	return t.hasLimits.Load()
}

// LimitedUserCount returns the number of users with active speed limits.
func (t *SpeedTracker) LimitedUserCount() int {
	t.mu.RLock()
	ids := make([]int, 0, len(t.buckets))
	for id := range t.buckets {
		ids = append(ids, id)
	}
	t.mu.RUnlock()

	t.limiter.mu.RLock()
	defer t.limiter.mu.RUnlock()
	count := 0
	for _, id := range ids {
		if u, ok := t.limiter.users[id]; ok && u.SpeedLimit > 0 {
			count++
		}
	}
	return count
}
