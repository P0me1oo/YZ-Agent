package model

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func RealityProtectionEnabled(setting any) bool {
	switch value := setting.(type) {
	case bool:
		return value
	case string:
		enabled, _ := strconv.ParseBool(strings.TrimSpace(value))
		return enabled
	case float64:
		return value != 0
	case int:
		return value != 0
	}
	return false
}

// RealityOrigin parses a network destination and supplies the default TLS port.
func RealityOrigin(value string, defaultPort int) (string, int, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "/\\@?# \t\r\n") {
		return "", 0, false
	}
	if host, rawPort, err := net.SplitHostPort(value); err == nil {
		port, err := strconv.Atoi(rawPort)
		return host, port, err == nil && host != "" && port > 0 && port <= 65535
	}
	if _, err := strconv.Atoi(value); err == nil {
		return "", 0, false // Xray 单端口目标沿用原生解析。
	}
	if strings.ContainsAny(value, ":[]") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
		if _, err := netip.ParseAddr(value); err != nil {
			return "", 0, false
		}
	}
	if defaultPort == 0 {
		defaultPort = 443
	}
	return value, defaultPort, defaultPort > 0 && defaultPort <= 65535
}
