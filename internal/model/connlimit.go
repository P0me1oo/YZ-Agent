package model

// 连接准入被拒绝的原因，上报面板时作为事件类型使用。
const (
	// ConnLimitKindConcurrent 表示超过并发连接数上限。
	ConnLimitKindConcurrent = "conn"
	// ConnLimitKindRate 表示超过每秒新建连接数上限。
	ConnLimitKindRate = "rate"
	// ConnLimitKindDevice 表示设备名额已满，新的公网来源被拒绝。
	ConnLimitKindDevice = "device"
)

// DeviceLimitReporter 记录设备数超限，随状态上报面板。
//
// 由 limiter 包实现，内核从已配置的 ConnLimiter 上断言获取。
// 单独成接口是为了不改动连接准入接口；只在拒绝新来源时调用，放行路径没有额外开销。
type DeviceLimitReporter interface {
	// ReportDeviceLimited 记录一次设备数超限拒绝。
	// limit 是该用户的设备上限，observed 是拒绝时已计入的来源数，sourceIP 是被拒的来源地址。
	ReportDeviceLimited(userID, limit, observed int, sourceIP string)
}

// ConnLimiter 由 limiter 包实现，内核在新连接建立时通过它做准入判断。
// 接口放在 model 层，避免内核包反向依赖 limiter 包。
//
// 三个方法都在每条新连接的热路径上调用，实现必须是并发安全的，
// 并且在没有任何用户配置连接限制时走无锁快速路径。
type ConnLimiter interface {
	// MaxConnByUserID 返回并发连接数上限；ok 为 false 表示该用户不限制并发。
	MaxConnByUserID(userID int) (limit int, ok bool)

	// AllowNewConn 消耗一个新建连接令牌，返回 false 表示超过每秒新建上限。
	// 未配置速率上限的用户恒返回 true。
	AllowNewConn(userID int) bool

	// ReportLimited 记录一次拒绝，kind 取 ConnLimitKind* 常量。
	// observed 是触发时的实测值（并发超限时为当前连接数），用于面板通知展示。
	// limit 传 0 表示内核不掌握该维度的配置值，由实现自行补全。
	ReportLimited(userID int, kind string, limit, observed int)
}
