package singbox

import "github.com/P0me1oo/YZ-Agent/internal/model"

func applySourcePolicy(cfg M, nc *model.NodeSpec) {
	if !nc.SourcePolicy.Enabled() || len(nc.SourceBlockCIDRs) == 0 {
		return
	}
	inbound := nc.Protocol + "-in"
	if nc.IsRelayLanding() {
		inbound = relayLandingInboundTag
	}
	route := cfg["route"].(M)
	rules, _ := route["rules"].([]M)
	// 直接拒绝，不依赖可被自定义覆盖的 block 出口，也不绕过后续中转与权限规则。
	route["rules"] = append([]M{{
		"inbound": []string{inbound}, "source_ip_cidr": append([]string(nil), nc.SourceBlockCIDRs...),
		"action": "reject",
	}}, rules...)
}
