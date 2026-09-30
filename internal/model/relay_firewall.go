package model

import "github.com/P0me1oo/YZ-Agent/internal/panel"

func cloneRelayFirewall(value *panel.RelayFirewallConfig) *panel.RelayFirewallConfig {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Sources = append([]string(nil), value.Sources...)
	return &copy
}
