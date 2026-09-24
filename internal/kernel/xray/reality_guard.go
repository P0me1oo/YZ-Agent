package xray

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// realityGuardTag 是防盗用专用入口的标签，路由规则据此把伪装回源和普通用户流量隔开。
const realityGuardTag = "reality-guard-in"

// 专用入口只监听本机，端口从动态端口段里挑，避开常见业务端口。
const (
	realityGuardPortBase    = 40000
	realityGuardPortSpan    = 20000
	realityGuardPortTries   = 64
	realityGuardDefaultPort = 443
)

// realityGuard 是一次防盗用改写的产物：一个本机专用入口和配套路由规则。
type realityGuard struct {
	inbound M
	rules   []M
}

// applyRealityGuard 在节点开启防盗用模式时，把 REALITY 的伪装回源地址改接到只监听本机的
// 专用入口，并返回该入口和配套路由规则；未开启或条件不足时返回 nil，保持原有行为。
//
// 要解决的问题：REALITY 服务端在读客户端首包之前就会先拨通回源地址，认证不通过的连接会
// 被原样双向转发过去。伪装域名若指向 CDN，任何人换一个 TLS 域名就能借这台机器访问该 CDN
// 上的其它站点，节点等于对外提供了一个通用 TLS 转发入口。改接本机专用入口后，只有伪装
// 域名列表里的 TLS 域名会被放行并按该域名回源，其余流量一律阻断。
func applyRealityGuard(nc *model.NodeSpec, inbound M) *realityGuard {
	if nc == nil || inbound == nil || !realityGuardEnabled(nc) {
		return nil
	}
	stream, ok := inbound["streamSettings"].(M)
	if !ok || stream["security"] != "reality" {
		return nil
	}
	settings, ok := stream["realitySettings"].(M)
	if !ok {
		return nil
	}

	domains := realityGuardDomains(settings["serverNames"])
	if len(domains) == 0 {
		nlog.Core().Warn("xray: REALITY 防盗用已开启但没有可用的伪装域名，保持原回源地址")
		return nil
	}

	// 回源端口沿用原来的伪装目标端口；嗅探只改写地址，端口由本入口决定。
	originPort := realityDestPort(settings["dest"])
	listenPort, err := allocateLoopbackPort(preferredGuardPort(nc.ServerPort), inboundPort(inbound))
	if err != nil {
		nlog.Core().Warn("xray: REALITY 防盗用无法分配本机端口，保持原回源地址", "error", err)
		return nil
	}
	settings["dest"] = fmt.Sprintf("127.0.0.1:%d", listenPort)

	guard := &realityGuard{
		inbound: M{
			"tag":      realityGuardTag,
			"listen":   "127.0.0.1",
			"port":     listenPort,
			"protocol": "dokodemo-door",
			"settings": M{
				// 嗅探成功后地址会被替换成实际 TLS 域名，这里只决定回源端口。
				"address": "127.0.0.1",
				"port":    originPort,
				"network": "tcp",
			},
			"sniffing": M{
				"enabled":      true,
				"destOverride": []string{"tls"},
				"routeOnly":    false,
			},
		},
		rules: []M{
			{
				"type":        "field",
				"inboundTag":  []string{realityGuardTag},
				"domain":      domains,
				"outboundTag": "direct",
			},
			{
				// 嗅不出 TLS 域名、或域名不在白名单里的连接都落到这里。
				"type":        "field",
				"inboundTag":  []string{realityGuardTag},
				"outboundTag": "block",
			},
		},
	}

	nlog.Core().Info("xray: REALITY 防盗用已启用",
		"listen", fmt.Sprintf("127.0.0.1:%d", listenPort),
		"allowed", strings.Join(domains, ","),
		"origin_port", originPort)
	return guard
}

// realityGuardEnabled 读取面板下发的防盗用开关。面板会把它规范成布尔值，
// 这里仍兼容字符串和数字写法，避免历史数据或手工配置被静默忽略。
func realityGuardEnabled(nc *model.NodeSpec) bool {
	if nc.TLS != 2 || nc.TLSSettings == nil {
		return false
	}
	switch v := nc.TLSSettings["anti_abuse"].(type) {
	case bool:
		return v
	case string:
		enabled, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && enabled
	case float64:
		return v != 0
	case int:
		return v != 0
	default:
		return false
	}
}

// realityGuardDomains 把伪装域名列表编译成精确匹配的路由域名。
// REALITY 内核本身也是按完整域名比对，这里保持同样口径。
func realityGuardDomains(serverNames any) []string {
	names, ok := serverNames.([]string)
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(names))
	domains := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		domains = append(domains, "full:"+name)
	}
	return domains
}

// realityDestPort 从原回源地址里取出端口，取不到时按 TLS 默认端口处理。
func realityDestPort(dest any) int {
	raw, ok := dest.(string)
	if !ok {
		return realityGuardDefaultPort
	}
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return realityGuardDefaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return realityGuardDefaultPort
	}
	return port
}

// preferredGuardPort 由节点端口推出一个固定起点，让同一节点重启后仍落在同一端口，
// 便于排查；真正可用与否由 allocateLoopbackPort 逐个试。
func preferredGuardPort(serverPort int) int {
	if serverPort < 0 {
		serverPort = -serverPort
	}
	return realityGuardPortBase + serverPort%realityGuardPortSpan
}

// inboundPort 取出节点自身入口占用的端口，分配本机端口时必须避开它：
// 节点入口通常监听通配地址，占用同一端口号会和本机入口冲突。
func inboundPort(inbound M) int {
	switch v := inbound["port"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

// allocateLoopbackPort 从 start 开始向后找一个本机可监听的端口。
func allocateLoopbackPort(start int, reserved int) (int, error) {
	for i := 0; i < realityGuardPortTries; i++ {
		port := realityGuardPortBase + (start-realityGuardPortBase+i)%realityGuardPortSpan
		if port == reserved {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		if err := listener.Close(); err != nil {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free loopback port in %d-%d", realityGuardPortBase, realityGuardPortBase+realityGuardPortSpan-1)
}
