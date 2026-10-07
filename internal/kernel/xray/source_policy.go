package xray

import (
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func applySourcePolicy(cfg M, nc *model.NodeSpec) {
	if !nc.SourcePolicy.Enabled() || len(nc.SourceBlockCIDRs) == 0 {
		return
	}
	// 单独生成丢弃出口，防止自定义的同名 block 出口改变策略含义。
	outbounds, _ := cfg["outbounds"].([]M)
	used := make(map[string]bool)
	for _, outbound := range outbounds {
		tag, _ := outbound["tag"].(string)
		used[tag] = true
	}
	tag := "yz-source-block"
	for i := 1; used[tag]; i++ {
		tag = fmt.Sprintf("yz-source-block-%d", i)
	}
	cfg["outbounds"] = append(outbounds, M{"tag": tag, "protocol": "blackhole"})
	inbound := nc.Protocol + "-in"
	if nc.IsRelayLanding() {
		inbound = relayLandingInboundTag
	}
	routing := cfg["routing"].(M)
	rules, _ := routing["rules"].([]M)
	// 放在所有自定义路由之前；只作用于当前代理入口，不影响 REALITY 的本机伪装回源。
	routing["rules"] = append([]M{{
		"type": "field", "inboundTag": []string{inbound},
		"source": append([]string(nil), nc.SourceBlockCIDRs...), "outboundTag": tag,
	}}, rules...)
}
