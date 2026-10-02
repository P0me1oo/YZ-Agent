package panel

import "time"

// TelemetryDemand 是主控确认中的短租约；旧主控或租约失效时保持原采样。
type TelemetryDemand struct {
	DetailInterval int  `json:"detail_interval"`
	UserSpeeds     bool `json:"user_speeds"`
	LeaseSeconds   int  `json:"lease_seconds"`
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

func (c *Client) acceptTelemetry(demand *TelemetryDemand, state StatePayload) {
	if state.Status != nil || state.Metrics != nil {
		c.realtime.lastDetail.Store(time.Now().UnixNano())
	}
	if demand == nil || (demand.DetailInterval != 1 && demand.DetailInterval != 60) || demand.LeaseSeconds < 5 || demand.LeaseSeconds > 60 {
		c.realtime.telemetry.Store(nil)
		return
	}
	c.realtime.telemetry.Store(&telemetryLease{demand: *demand, until: time.Now().Add(time.Duration(demand.LeaseSeconds) * time.Second)})
}
