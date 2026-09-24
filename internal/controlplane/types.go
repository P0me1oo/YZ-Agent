package controlplane

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type EventType string

const (
	EventSyncConfig    EventType = "sync.config"
	EventSyncUsers     EventType = "sync.users"
	EventSyncUserDelta EventType = "sync.user.delta"
	EventSyncDevices   EventType = "sync.devices"
)

type Event struct {
	Type        EventType
	Config      *model.NodeSpec
	Users       []model.UserSpec
	DeltaAction string
	DeltaUsers  []model.UserSpec
	DeviceUsers map[int][]string
}

type StatusChange struct {
	Connected   bool
	NeedsResync bool
}

type APIMetrics struct {
	Success uint64
	Failure uint64
}

type Bootstrap struct {
	PushInterval int
	PullInterval int
	Push         PushClient
	Config       *model.NodeSpec
	Users        []model.UserSpec
	Mailbox      *NodeMailbox
}

type Snapshot struct {
	Config *model.NodeSpec
	Users  []model.UserSpec
}

// PollStateResetter 强制下一次 REST 对账返回完整配置和用户快照。
type PollStateResetter interface {
	ResetPollingState()
}

type ReportPayload struct {
	// ReportID 标识一次刷出的报告批次，失败重试时复用同一 ID。
	// 为空时兼容未提供批次标识的旧调用方。
	ReportID string
	Traffic  map[int][2]int64
	// RelayTraffic 是入口节点上各中转逻辑节点独立出站的流量，键为逻辑节点 ID。
	// 只作为落地线路运营统计，面板不据此扣除用户套餐。
	RelayTraffic map[int][2]int64
	// RelayUserTraffic 是入口节点按用户和逻辑落地节点拆分的流量。
	// 它只用于归属明细，不参与用户套餐扣除，也不替代 RelayTraffic。
	RelayUserTraffic map[int]map[int][2]int64
	Alive            map[int][]string
	Online           map[int]int
	CPU              float64
	Mem              [2]uint64
	Swap             [2]uint64
	Disk             [2]uint64
	Metrics          map[string]interface{}
	// LimitEvents 是本周期内触发连接数、新建速率或设备数上限的用户汇总，无事件时为空。
	LimitEvents []panel.LimitEvent
}

type PushClient interface {
	Run(ctx context.Context)
	IsConnected() bool
	SendDeviceReport(devices map[int][]string)
}

type Source interface {
	Initial(ctx context.Context, metricsFn func() map[string]interface{}, events chan<- Event, statuses chan<- StatusChange) (Bootstrap, error)
	Poll(ctx context.Context) (Snapshot, error)
	Discover(ctx context.Context, metricsFn func() map[string]interface{}, events chan<- Event, statuses chan<- StatusChange) (PushClient, error)
	Metrics() APIMetrics
	SupportsPolling() bool
	SupportsDiscovery() bool
}

type Sink interface {
	Report(payload ReportPayload) error
	ReportDevices(push PushClient, devices map[int][]string)
	SupportsReporting() bool
	SupportsDeviceReports() bool
}

type ControlPlane interface {
	Source
	Sink
}
