package monitor

import (
	"os"
	"strings"
)

// TCPCongestion reads the system default, independently of load monitoring.
// It deliberately never loads modules or changes any sysctl.
func TCPCongestion() string {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/tcp_congestion_control")
	if err != nil {
		return ""
	}
	return congestionName(string(raw))
}
func congestionName(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 {
		return ""
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return ""
		}
	}
	return strings.ToLower(value)
}
