// Package limitreport 定义超限通知使用的独立传输结构，避免面板客户端和模型循环依赖。
package limitreport

// Snapshot 是同一次拒绝的来源、时间和名额快照。
type Snapshot struct {
	OccurredAt  int64    `json:"occurred_at"`
	Limit       int      `json:"limit"`
	Observed    int      `json:"observed"`
	SourceIP    string   `json:"source_ip"`
	ActiveIPs   []string `json:"active_ips"`
	ActiveTotal int      `json:"active_total"`
	ActiveKnown bool     `json:"active_known"`
}
