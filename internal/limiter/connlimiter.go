package limiter

import (
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
	PeakConn  int    // 本周期触发并发拒绝时，已占用连接名额的最大值
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
	l.limitEventsMu.Lock()
	for id := range l.limitEvents {
		if _, exists := users[id]; !exists {
			delete(l.limitEvents, id)
		}
	}
	l.limitEventsMu.Unlock()
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
	if kind != model.ConnLimitKindConcurrent && kind != model.ConnLimitKindRate {
		return
	}
	// 与用户刷新串行，避免删号后迟到的拒绝事件重新创建记录。
	l.mu.RLock()
	defer l.mu.RUnlock()
	if _, exists := l.users[userID]; !exists {
		return
	}
	if limit <= 0 {
		spec, ok := l.connLimits[userID]
		if ok {
			switch kind {
			case model.ConnLimitKindConcurrent:
				limit = spec.maxConn
			case model.ConnLimitKindRate:
				limit = spec.ratePerSec
			}
		}
	}

	l.limitEventsMu.Lock()
	defer l.limitEventsMu.Unlock()
	stat := l.limitEvents[userID]
	switch kind {
	case model.ConnLimitKindConcurrent:
		stat.ConnHits++
		stat.ConnLimit = limit
		if observed > stat.PeakConn {
			stat.PeakConn = observed
		}
	case model.ConnLimitKindRate:
		stat.RateHits++
		stat.RateLimit = limit
	}
	l.limitEvents[userID] = stat
	l.connLimitEvents.Add(1)
}

// SnapshotLimitEvents 取出并清理本周期的超限统计，供上报面板使用。
// 本周期没有触发超限的用户不会出现在结果里。
func (l *Limiter) SnapshotLimitEvents() map[int]LimitEventStat {
	l.limitEventsMu.Lock()
	out := l.limitEvents
	l.limitEvents = make(map[int]LimitEventStat)
	l.limitEventsMu.Unlock()
	return out
}
