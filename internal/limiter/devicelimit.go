package limiter

import (
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
	l.ReportLimitedSnapshot(userID, model.ConnLimitKindDevice, limit, observed, sourceIP, nil)
}
