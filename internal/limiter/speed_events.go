package limiter

import "time"

// 令牌欠额来自核心已经执行的带宽预留；仅配置限速但没有实际欠额时不记事件。
// 按秒采样，不在数据包路径新增锁、日志写入或网络请求。
func (t *SpeedTracker) SampleThrottled(at time.Time) {
	if !t.hasLimits.Load() {
		return
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.limiter.mu.RLock()
	defer t.limiter.mu.RUnlock()
	for id, bucket := range t.buckets {
		user, exists := t.limiter.users[id]
		if !exists || user.SpeedLimit <= 0 || bucket.TokensAt(at) >= 0 {
			continue
		}
		t.limiter.limitEventsMu.Lock()
		stat := t.limiter.limitEvents[id]
		stat.SpeedHits++
		stat.SpeedLimit = user.SpeedLimit
		stat.SpeedLast = at.Unix()
		if stat.SpeedFirst == 0 {
			stat.SpeedFirst = at.Unix()
		}
		t.limiter.limitEvents[id] = stat
		t.limiter.limitEventsMu.Unlock()
	}
}
