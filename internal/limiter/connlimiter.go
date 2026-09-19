package limiter

import (
	"sync/atomic"

	"golang.org/x/time/rate"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// Limiter 承担内核的连接准入判断。
var _ model.ConnLimiter = (*Limiter)(nil)

// connLimitSpec 是单个用户的连接限制配置，0 表示对应维度不限制。
type connLimitSpec struct {
	maxConn    int
	ratePerSec int
}

// LimitEventStat 是单个用户在一个上报周期内的超限统计。
type LimitEventStat struct {
	ConnHits  uint64 // 并发超限被拒次数
	RateHits  uint64 // 新建速率超限被拒次数
	ConnLimit int    // 触发时生效的并发上限
	RateLimit int    // 触发时生效的速率上限
	PeakConn  int    // 周期内观测到的并发峰值
}

// limitEventCounter 累积单个用户的超限次数。
// 全部用原子操作，热路径上不加锁。
type limitEventCounter struct {
	connHits  atomic.Uint64
	rateHits  atomic.Uint64
	connLimit atomic.Int64
	rateLimit atomic.Int64
	peakConn  atomic.Int64
}

// rebuildConnLimitsLocked 依据最新用户列表重建限制索引和令牌桶。
// 调用方必须持有 l.mu 写锁。
//
// 已存在的令牌桶原地调速而不是重建，否则每次配置刷新都会把桶填满，
// 正在被限速的用户会拿到一次完整突发额度。
func (l *Limiter) rebuildConnLimitsLocked(users map[int]model.UserSpec) {
	limits := make(map[int]connLimitSpec, len(users))
	buckets := make(map[int]*rate.Limiter, len(l.connRateBuckets))

	for id, u := range users {
		spec := connLimitSpec{maxConn: u.ConnLimit, ratePerSec: u.ConnRateLimit}
		if spec.maxConn <= 0 && spec.ratePerSec <= 0 {
			continue
		}
		limits[id] = spec
		if spec.ratePerSec <= 0 {
			continue
		}
		// 桶容量取 1 秒额度，容纳客户端打开页面时的正常突发。
		burst := spec.ratePerSec
		if existing, ok := l.connRateBuckets[id]; ok {
			existing.SetLimit(rate.Limit(spec.ratePerSec))
			existing.SetBurst(burst)
			buckets[id] = existing
			continue
		}
		buckets[id] = rate.NewLimiter(rate.Limit(spec.ratePerSec), burst)
	}

	l.connLimits = limits
	l.connRateBuckets = buckets
	l.hasConnLimits.Store(len(limits) > 0)

	// 清掉已经不在用户列表里的统计，避免 map 随用户变更无限增长。
	l.limitEvents.Range(func(key, _ any) bool {
		if id, ok := key.(int); ok {
			if _, exists := users[id]; !exists {
				l.limitEvents.Delete(key)
			}
		}
		return true
	})
}

// MaxConnByUserID 返回并发连接数上限；ok 为 false 表示该用户不限制并发。
func (l *Limiter) MaxConnByUserID(userID int) (int, bool) {
	if !l.hasConnLimits.Load() {
		return 0, false
	}
	l.mu.RLock()
	spec, ok := l.connLimits[userID]
	l.mu.RUnlock()
	if !ok || spec.maxConn <= 0 {
		return 0, false
	}
	return spec.maxConn, true
}

// AllowNewConn 消耗一个新建连接令牌，返回 false 表示超过每秒新建上限。
func (l *Limiter) AllowNewConn(userID int) bool {
	if !l.hasConnLimits.Load() {
		return true
	}
	l.mu.RLock()
	bucket := l.connRateBuckets[userID]
	l.mu.RUnlock()
	if bucket == nil {
		return true
	}
	return bucket.Allow()
}

// ReportLimited 记录一次拒绝，供上报面板和指标统计使用。
// limit 传 0 时由 limiter 自己补全配置值，内核不需要知道速率上限。
func (l *Limiter) ReportLimited(userID int, kind string, limit, observed int) {
	if limit <= 0 {
		l.mu.RLock()
		spec, ok := l.connLimits[userID]
		l.mu.RUnlock()
		if ok {
			switch kind {
			case model.ConnLimitKindConcurrent:
				limit = spec.maxConn
			case model.ConnLimitKindRate:
				limit = spec.ratePerSec
			}
		}
	}

	value, _ := l.limitEvents.LoadOrStore(userID, &limitEventCounter{})
	counter, ok := value.(*limitEventCounter)
	if !ok {
		return
	}
	switch kind {
	case model.ConnLimitKindConcurrent:
		counter.connHits.Add(1)
		counter.connLimit.Store(int64(limit))
		// 记录周期内的并发峰值，通知里用它说明超限时的实际规模。
		for {
			peak := counter.peakConn.Load()
			if int64(observed) <= peak || counter.peakConn.CompareAndSwap(peak, int64(observed)) {
				break
			}
		}
	case model.ConnLimitKindRate:
		counter.rateHits.Add(1)
		counter.rateLimit.Store(int64(limit))
	default:
		return
	}
	l.connLimitEvents.Add(1)
}

// SnapshotLimitEvents 取出并归零本周期的超限统计，供上报面板使用。
// 本周期没有触发超限的用户不会出现在结果里。
func (l *Limiter) SnapshotLimitEvents() map[int]LimitEventStat {
	out := make(map[int]LimitEventStat)
	l.limitEvents.Range(func(key, value any) bool {
		counter, ok := value.(*limitEventCounter)
		if !ok {
			return true
		}
		userID, ok := key.(int)
		if !ok {
			return true
		}
		connHits := counter.connHits.Swap(0)
		rateHits := counter.rateHits.Swap(0)
		peak := counter.peakConn.Swap(0)
		if connHits == 0 && rateHits == 0 {
			return true
		}
		out[userID] = LimitEventStat{
			ConnHits:  connHits,
			RateHits:  rateHits,
			ConnLimit: int(counter.connLimit.Load()),
			RateLimit: int(counter.rateLimit.Load()),
			PeakConn:  int(peak),
		}
		return true
	})
	return out
}
