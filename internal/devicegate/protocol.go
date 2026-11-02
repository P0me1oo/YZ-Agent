package devicegate

import (
	"context"
	"errors"
	"github.com/P0me1oo/YZ-Agent/internal/limitreport"
)

var (
	ErrSessionLost              = errors.New("设备来源会话已失效")
	ErrSnapshotRequired         = errors.New("设备来源需要完整快照")
	ErrAdmissionSyncUnsupported = errors.New("面板不支持合并设备申请")
	ErrNotReady                 = errors.New("设备来源协调尚未就绪")
	ErrRevoked                  = errors.New("设备来源已被替换")
)

type BeginReply struct {
	Version    int    `json:"version"`
	Run        string `json:"run"`
	LeaseCache int    `json:"lease_cache"`
}

type AdmissionRequest struct {
	LeaseCache int       `json:"lease_cache,omitempty"`
	Run        string    `json:"run"`
	Sequence   uint64    `json:"sequence"`
	UserID     int       `json:"user_id"`
	IP         string    `json:"ip"`
	Snapshot   *Snapshot `json:"snapshot,omitempty"`
}

type AdmissionReply struct {
	ConfirmedSequence uint64                `json:"confirmed_sequence,omitempty"`
	ConfirmedAt       int64                 `json:"confirmed_at"`
	ExpiresAt         int64                 `json:"expires_at"`
	ServerTime        int64                 `json:"server_time"`
	Status            string                `json:"status"`
	Lease             string                `json:"lease"`
	Limit             int                   `json:"limit"`
	Observed          int                   `json:"observed"`
	Reason            string                `json:"reason"`
	Revoked           []Revocation          `json:"revoked"`
	LimitSnapshot     *limitreport.Snapshot `json:"limit_snapshot,omitempty"`
	Sync              *SyncReply            `json:"sync,omitempty"`
}

type Source struct {
	UserID          int    `json:"user_id"`
	IP              string `json:"ip"`
	Lease           string `json:"lease"`
	ConnectSequence uint64 `json:"connect_sequence"`
	AgeMS           int64  `json:"age_ms"`
}

type Snapshot struct {
	LeaseCache   int          `json:"lease_cache,omitempty"`
	Unchanged    bool         `json:"unchanged,omitempty"`
	BaseSequence uint64       `json:"base_sequence,omitempty"`
	Run          string       `json:"run"`
	Sequence     uint64       `json:"sequence"`
	Pending      []uint64     `json:"pending"`
	Sources      []Source     `json:"sources"`
	Retired      []Revocation `json:"retired"`
}

type SyncReply struct {
	Idle       []Revocation `json:"idle,omitempty"`
	LeaseCache int          `json:"lease_cache"`
	ServerTime int64        `json:"server_time"`
	Leases     []Lease      `json:"leases"`
	Run        string       `json:"run"`
	Sequence   uint64       `json:"sequence"`
	Revoked    []Revocation `json:"revoked"`
}

type Lease struct {
	ConfirmedSequence uint64 `json:"confirmed_sequence,omitempty"`
	UserID            int    `json:"user_id"`
	IP                string `json:"ip"`
	Lease             string `json:"lease"`
	ConfirmedAt       int64  `json:"confirmed_at"`
	ExpiresAt         int64  `json:"expires_at"`
}

type Revocation struct {
	Run               string `json:"run,omitempty"`
	ConfirmedSequence uint64 `json:"confirmed_sequence,omitempty"`
	UserID            int    `json:"user_id"`
	IP                string `json:"ip"`
	Lease             string `json:"lease"`
}

type Remote interface {
	DeviceHandoverSupported() bool
	BeginDeviceSession(context.Context, string) (BeginReply, error)
	AdmitDeviceSource(context.Context, AdmissionRequest) (AdmissionReply, error)
	SyncDeviceSession(context.Context, Snapshot) (SyncReply, error)
}

// RenewalRemote 是可选能力，旧主控和原有协调实现继续使用完整快照。
type RenewalRemote interface {
	DeviceRenewalSupported() bool
}

// AdmissionSyncRemote 可选地在同一请求内先确认完整状态，再申请名额。
type AdmissionSyncRemote interface {
	DeviceAdmissionSyncSupported() bool
}

type DeniedError struct {
	Limit, Observed int
	Reason          string
	Snapshot        *limitreport.Snapshot
}

func (*DeniedError) Error() string { return "设备来源名额暂不可用" }
