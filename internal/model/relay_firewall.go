package model

import "github.com/P0me1oo/YZ-Agent/internal/panel"

func (n *NodeSpec) HasRelayFirewall() bool {
	return n.IsRelayLanding() && n.Relay.Firewall != nil && n.Relay.Firewall.Version != 0
}

func cloneRelayFirewall(value *panel.RelayFirewallConfig) *panel.RelayFirewallConfig {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Sources = append([]string(nil), value.Sources...)
	return &copy
}
