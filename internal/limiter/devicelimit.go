package limiter

import (
	"slices"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// Limiter 同时记录设备数超限，供上报面板使用。
var _ model.DeviceLimitReporter = (*Limiter)(nil)

// MaxDeviceEventIPs 是单个用户在一个上报周期内最多保留的被拒来源数。
// 客户端被拒后会反复重连，只保留去重后的前几个地址，上报体积不随重试次数增长。
const MaxDeviceEventIPs = 5

// ReportDeviceLimited 记录一次设备数超限拒绝，并计入累计指标。
// limit 传 0 时由 limiter 按当前用户配置补全。
func (l *Limiter) ReportDeviceLimited(userID, limit, observed int, sourceIP string) {
	// 与用户刷新串行，避免删号后迟到的拒绝事件重新创建记录。
	l.mu.RLock()
	defer l.mu.RUnlock()
	user, exists := l.users[userID]
	if !exists {
		return
	}
	if limit <= 0 {
		limit = user.DeviceLimit
	}

	l.limitEventsMu.Lock()
	defer l.limitEventsMu.Unlock()
	stat := l.limitEvents[userID]
	stat.DeviceHits++
	stat.DeviceLimit = limit
	if observed > stat.PeakDevices {
		stat.PeakDevices = observed
	}
	if sourceIP != "" && len(stat.DeviceIPs) < MaxDeviceEventIPs && !slices.Contains(stat.DeviceIPs, sourceIP) {
		stat.DeviceIPs = append(stat.DeviceIPs, sourceIP)
	}
	l.limitEvents[userID] = stat
	l.deviceLimitEvents.Add(1)
}
