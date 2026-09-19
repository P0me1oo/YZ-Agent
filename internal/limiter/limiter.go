package limiter

import (
	"sync"
	"sync/atomic"

	"golang.org/x/time/rate"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// Limiter enforces per-user device limits and detects removed users.
type Limiter struct {
	mu    sync.RWMutex
	users map[int]model.UserSpec

	// uuidDeviceLimit provides O(1) UUID→device-limit lookup for the
	// kernel gate-keeping hot path (called per new connection).
	uuidDeviceLimit map[string]int

	// connLimits 和 connRateBuckets 按 userID 索引连接数限制，受 mu 保护。
	// 内核每建立一条连接都会查一次，hasConnLimits 提供无锁快速路径。
	connLimits      map[int]connLimitSpec
	connRateBuckets map[int]*rate.Limiter
	hasConnLimits   atomic.Bool

	// limitEvents 按 userID 累积超限次数，上报周期结束时快照并归零。
	limitEvents sync.Map

	deviceLimitEvents atomic.Uint64
	connLimitEvents   atomic.Uint64
}

func New() *Limiter {
	return &Limiter{
		users:           make(map[int]model.UserSpec),
		uuidDeviceLimit: make(map[string]int),
		connLimits:      make(map[int]connLimitSpec),
		connRateBuckets: make(map[int]*rate.Limiter),
	}
}

// UpdateUsers refreshes the user limit configuration and returns
// the IDs of users that were present before but are now removed.
func (l *Limiter) UpdateUsers(users []model.UserSpec) []int {
	l.mu.Lock()
	defer l.mu.Unlock()

	newUsers := make(map[int]model.UserSpec, len(users))
	for _, u := range users {
		newUsers[u.ID] = u
	}

	// Find removed users
	var removed []int
	for id := range l.users {
		if _, ok := newUsers[id]; !ok {
			removed = append(removed, id)
		}
	}

	l.users = newUsers

	// Rebuild UUID→device-limit index for O(1) lookups.
	idx := make(map[string]int, len(newUsers))
	for _, u := range newUsers {
		if u.DeviceLimit > 0 {
			idx[u.UUID] = u.DeviceLimit
		}
	}
	l.uuidDeviceLimit = idx

	l.rebuildConnLimitsLocked(newUsers)

	return removed
}

// LimiterMetrics holds aggregated limiter statistics.
type LimiterMetrics struct {
	// DeviceLimitEvents is the total number of times a user exceeded
	// the configured device limit (since process start).
	DeviceLimitEvents uint64
	// ConnLimitEvents 是连接数或新建速率超限被拒的累计次数（进程启动以来）。
	ConnLimitEvents uint64
}

// SnapshotMetrics returns a snapshot of limiter metrics.
func (l *Limiter) SnapshotMetrics() LimiterMetrics {
	return LimiterMetrics{
		DeviceLimitEvents: l.deviceLimitEvents.Load(),
		ConnLimitEvents:   l.connLimitEvents.Load(),
	}
}

// GetDeviceLimitByUUID returns the device limit for a user identified by UUID.
// Returns (limit, true) if a device limit is set, (0, false) otherwise.
// This is designed to be passed as a function reference to kernel.SetDeviceLimitFunc.
func (l *Limiter) GetDeviceLimitByUUID(uuid string) (int, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	limit, ok := l.uuidDeviceLimit[uuid]
	return limit, ok
}
