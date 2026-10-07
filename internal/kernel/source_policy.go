package kernel

import (
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// 来源策略开启时，运行层必须先准备有效网段，不能降级成无拦截启动。
func ValidateSourcePolicy(nc *model.NodeSpec) error {
	if nc == nil {
		return nil
	}
	if err := nc.SourcePolicy.Validate(); err != nil {
		return err
	}
	if nc.SourcePolicy.Enabled() && nc.IsRelayLanding() && nc.Relay.Protocol == "wireguard" {
		return fmt.Errorf("WireGuard 暂不支持大陆来源拦截")
	}
	if nc != nil && nc.SourcePolicy.Enabled() && !nc.SourcePolicyReady {
		return fmt.Errorf("大陆来源拦截未准备完成")
	}
	return nil
}
