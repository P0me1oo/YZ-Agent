package model

import (
	"encoding/json"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"gopkg.in/yaml.v3"
	"net/netip"
	"slices"
	"strings"
)

// Versioned Next policies never inherit the legacy trust-all PROXY behavior.
func validateAccessPolicy(n *NodeSpec) error {
	if n.AccessPolicyVersion == 0 {
		return nil
	}
	if n.AccessPolicyVersion != 1 {
		return fmt.Errorf("不支持的访问策略版本，请升级 Agent")
	}
	if n.GetProxyProtocol() && len(n.TrustedProxyIPs) == 0 {
		return fmt.Errorf("PROXY 接收缺少可信来源")
	}
	for _, list := range [][]string{n.TrustedProxyIPs, n.DeviceIPExclude} {
		if len(list) > 128 {
			return fmt.Errorf("来源名单超过容量")
		}
		for _, value := range list {
			if ip, err := netip.ParseAddr(value); err == nil {
				if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
					return fmt.Errorf("来源 IP 无效")
				}
			} else if p, err := netip.ParsePrefix(value); err != nil || p.Bits() == 0 || p.Addr().Is4In6() || p.Addr().IsMulticast() {
				return fmt.Errorf("来源网段无效")
			}
		}
	}
	if n.IsRelayLanding() && (n.SourcePolicy.Enabled() || n.GetProxyProtocol() || len(n.DeviceIPExclude) > 0) {
		return fmt.Errorf("内部中转来源策略须设置在入口")
	}
	if n.SourcePolicy != nil {
		p := n.SourcePolicy
		switch p.Mode {
		case "", "disabled":
			if p.BlockCN || len(p.AllowIPs)+len(p.BlockIPs) > 0 {
				return fmt.Errorf("停用来源策略包含有效条件")
			}
		case "whitelist":
			if p.BlockCN || len(p.BlockIPs) > 0 {
				return fmt.Errorf("白名单不能包含黑名单或地区屏蔽")
			}
		case "blacklist":
			if !p.BlockCN && len(p.AllowIPs) > 0 {
				return fmt.Errorf("来源例外需要地区屏蔽")
			}
			for _, ip := range p.AllowIPs {
				if slices.Contains(p.BlockIPs, ip) {
					return fmt.Errorf("来源允许与屏蔽冲突")
				}
			}
		}
	}
	return nil
}

func validateNextLocalRoutes(path string) error {
	if path == "" {
		return nil
	}
	data, err := privacy.ReadFile(path)
	if err != nil {
		return err
	}
	var config map[string]any
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		err = json.Unmarshal(data, &config)
	} else {
		err = yaml.Unmarshal(data, &config)
	}
	if err != nil {
		return fmt.Errorf("读取本地配置失败: %w", err)
	}
	for _, key := range []string{"routing", "route"} {
		if value, ok := config[key]; ok && value != nil {
			return fmt.Errorf("Next 服务端路由不能与本地配置中的路由覆盖混用")
		}
	}
	return nil
}
