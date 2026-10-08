package devicegate

import (
	"context"
	"errors"
)

var (
	ErrSessionLost = errors.New("设备来源会话已失效")
	ErrNotReady    = errors.New("设备来源协调尚未就绪")
	ErrRevoked     = errors.New("设备来源已被替换")
)

type BeginReply struct {
	Version int    `json:"version"`
	Run     string `json:"run"`
}

type AdmissionRequest struct {
	Run      string `json:"run"`
	Sequence uint64 `json:"sequence"`
	UserID   int    `json:"user_id"`
	IP       string `json:"ip"`
}

type AdmissionReply struct {
	Status   string       `json:"status"`
	Lease    string       `json:"lease"`
	Limit    int          `json:"limit"`
	Observed int          `json:"observed"`
	Reason   string       `json:"reason"`
	Revoked  []Revocation `json:"revoked"`
}

type Source struct {
	UserID          int    `json:"user_id"`
	IP              string `json:"ip"`
	Lease           string `json:"lease"`
	ConnectSequence uint64 `json:"connect_sequence"`
	AgeMS           int64  `json:"age_ms"`
}

type Snapshot struct {
	Run      string       `json:"run"`
	Sequence uint64       `json:"sequence"`
	Pending  []uint64     `json:"pending"`
	Sources  []Source     `json:"sources"`
	Retired  []Revocation `json:"retired"`
}

type SyncReply struct {
	Run      string       `json:"run"`
	Sequence uint64       `json:"sequence"`
	Revoked  []Revocation `json:"revoked"`
}

type Revocation struct {
	UserID int    `json:"user_id"`
	IP     string `json:"ip"`
	Lease  string `json:"lease"`
}

type Remote interface {
	DeviceHandoverSupported() bool
	BeginDeviceSession(context.Context, string) (BeginReply, error)
	AdmitDeviceSource(context.Context, AdmissionRequest) (AdmissionReply, error)
	SyncDeviceSession(context.Context, Snapshot) (SyncReply, error)
}

type DeniedError struct {
	Limit, Observed int
	Reason          string
}

func (*DeniedError) Error() string { return "设备来源名额暂不可用" }
