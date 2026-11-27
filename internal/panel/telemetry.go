package panel

import (
	"slices"
	"time"
)

// TelemetryDemand 是主控确认中的短租约；旧主控或租约失效时保持原采样。
type TelemetryDemand struct {
	UserUsageVersion  int   `json:"user_usage_version,omitempty"`
	UserUsageIDs      []int `json:"user_usage_ids,omitempty"`
	DetailInterval    int   `json:"detail_interval"`
	UserSpeeds        bool  `json:"user_speeds"`
	OmitDeviceDetails bool  `json:"omit_device_details,omitempty"`
	LeaseSeconds      int   `json:"lease_seconds"`
}

type telemetryLease struct {
	demand TelemetryDemand
	until  time.Time
}

// TelemetryNeeds 只决定展示采样，不能跳过设备、连接统计或可靠流量上报。
func (c *Client) TelemetryNeeds() (details, speeds bool) {
	lease := c.realtime.telemetry.Load()
	if lease == nil || !time.Now().Before(lease.until) {
		return true, true
	}
	last := c.realtime.lastDetail.Load()
	return lease.demand.DetailInterval == 1 || last == 0 || time.Since(time.Unix(0, last)) >= time.Duration(lease.demand.DetailInterval)*time.Second, lease.demand.UserSpeeds
}

// OmitDeviceDetails applies only to the current lease's state snapshots. The
// device admission protocol, local tracking and traffic batches run independently.
func (c *Client) OmitDeviceDetails() bool {
	lease := c.realtime.telemetry.Load()
	return lease != nil && lease.demand.OmitDeviceDetails && time.Now().Before(lease.until)
}

func (c *Client) acceptTelemetry(demand *TelemetryDemand, state StatePayload) {
	if state.Status != nil || state.Metrics != nil {
		c.realtime.lastDetail.Store(time.Now().UnixNano())
	}
	if demand == nil || (demand.DetailInterval != 1 && demand.DetailInterval != 60) || demand.LeaseSeconds < 5 || demand.LeaseSeconds > 60 {
		c.realtime.telemetry.Store(nil)
		return
	}
	next := *demand
	// Older panels omit the version. Malformed selections cannot enable data collection.
	if next.UserUsageVersion == 1 && next.OmitDeviceDetails && !next.UserSpeeds && len(next.UserUsageIDs) <= 64 {
		next.UserUsageIDs = slices.Clone(next.UserUsageIDs)
		slices.Sort(next.UserUsageIDs)
		next.UserUsageIDs = slices.Compact(next.UserUsageIDs)
		if len(next.UserUsageIDs) > 0 && next.UserUsageIDs[0] <= 0 {
			next.UserUsageVersion = 0
		}
	} else {
		next.UserUsageVersion = 0
	}
	c.realtime.telemetry.Store(&telemetryLease{demand: next, until: time.Now().Add(time.Duration(demand.LeaseSeconds) * time.Second)})
}

// UserUsageDemand returns the immutable selection in the most recent, valid node lease.
func (c *Client) UserUsageDemand() ([]int, bool) {
	lease := c.realtime.telemetry.Load()
	if lease == nil || lease.demand.UserUsageVersion != 1 || !time.Now().Before(lease.until) {
		return nil, false
	}
	return lease.demand.UserUsageIDs, true
}
