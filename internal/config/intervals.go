package config

import "fmt"

// ValidateIntervals 校验秒数；零值保留默认值或面板协商的语义。
func ValidateIntervals(node NodeConfig, ws WSConfig) error {
	for _, item := range []struct {
		name         string
		value, limit int
	}{
		{"node.push_interval", node.PushInterval, 86400},
		{"node.pull_interval", node.PullInterval, 86400},
		{"node.track_interval", node.TrackInterval, 86400},
		{"node.device_report_interval", node.DeviceReportInterval, 86400},
		{"ws.status_interval", ws.StatusInterval, 86400},
		{"ws.discovery_interval", ws.DiscoveryInterval, 86400},
		{"ws.handshake_timeout", ws.HandshakeTimeout, 300},
		{"ws.backoff_initial", ws.BackoffInitial, 3600},
		{"ws.backoff_max", ws.BackoffMax, 3600},
	} {
		if item.value < 0 || item.value > item.limit {
			return fmt.Errorf("%s must be between 0 and %d seconds", item.name, item.limit)
		}
	}
	initial, maximum := ws.BackoffInitial, ws.BackoffMax
	if initial == 0 {
		initial = 1
	}
	if maximum == 0 {
		maximum = 60
	}
	if initial > maximum {
		return fmt.Errorf("ws.backoff_initial must not exceed ws.backoff_max")
	}
	return nil
}
