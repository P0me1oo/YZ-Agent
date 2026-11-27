package panel

// A nil metric is unknown. An empty IP list and a zero speed are observations.
type UserUsageBatch struct {
	SampledAt int64             `json:"sampled_at"`
	Users     map[int]UserUsage `json:"users"`
}

type UserUsage struct {
	IPs            []string        `json:"ips"`
	Upload         *int64          `json:"upload_bps,omitempty"`
	Download       *int64          `json:"download_bps,omitempty"`
	Connections    *int            `json:"connections,omitempty"`
	TemporaryLimit *TemporaryLimit `json:"temporary_limit,omitempty"`
	LimitKnown     bool            `json:"limit_known"`
}

type TemporaryLimit struct {
	Mbps  int   `json:"mbps"`
	Until int64 `json:"until"`
}
