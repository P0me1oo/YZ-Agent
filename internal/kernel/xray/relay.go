package xray

import (
	"context"
	"net"
	"strconv"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	xnet "github.com/xtls/xray-core/common/net"
)

// relayLandingInboundTag is the tag of the internal inbound on a landing node.
// It is distinct from the client-facing inbound tag so stats and
// logs never mix the two.
const relayLandingInboundTag = "relay-in"

// buildRelayOutbounds returns one internal outbound per logical node.
//
// Each outbound carries its own internal credential and is selected on the entry
// node by a `vlessRoute` routing rule, so the entry keeps a single client inbound
// while every logical node gets an independent exit.
func buildRelayOutbounds(nc *model.NodeSpec) []M {
	if !nc.IsRelayEntry() {
		return nil
	}

	outbounds := make([]M, 0, len(nc.Relay.Children))
	for _, child := range nc.Relay.Children {
		switch strings.ToLower(strings.TrimSpace(child.Protocol)) {
		case "wireguard":
			if child.WireGuard == nil {
				continue
			}
			settings := relayWireGuardSettings(child.WireGuard)
			settings["peers"].([]M)[0]["endpoint"] = net.JoinHostPort(child.Address, strconv.Itoa(child.Port))
			outbounds = append(outbounds, M{"protocol": "wireguard", "tag": child.Tag, "settings": settings})
		case "shadowsocks":
			outbounds = append(outbounds, M{
				"protocol": "shadowsocks",
				"tag":      child.Tag,
				"settings": M{
					"servers": []M{{
						"address":  child.Address,
						"port":     child.Port,
						"method":   child.Cipher,
						"password": child.Password,
					}},
				},
			})
		case "vless":
			if child.VLESS == nil {
				continue
			}
			user := M{
				"id":         child.VLESS.ID,
				"encryption": child.VLESS.Encryption,
			}
			if child.VLESS.Flow != "" {
				user["flow"] = child.VLESS.Flow
			}
			outbounds = append(outbounds, M{
				"protocol": "vless",
				"tag":      child.Tag,
				"settings": M{
					"vnext": []M{{
						"address": child.Address,
						"port":    child.Port,
						"users":   []M{user},
					}},
				},
				"streamSettings": buildRelayVLESSClientStream(child.VLESS),
			})
		}
	}
	return outbounds
}

// buildRelayRoutingRules 把 VLESS 身份或 HY2 认证 UUID 中的路由编号映射到出站。
// Xray 清零 UUID 的第 7、8 字节后校验用户，再用原始字节填充 vlessRoute。
// 第一个返回值是落地编号选择内部出站的规则，必须排在面板路由之前；
// 第二个返回值是入口自身编号选择直接出站的规则，排在面板路由之后，
// 这样入口绑定的面板路由对入口自身用户生效，未命中时仍固定走直连。
func buildRelayRoutingRules(nc *model.NodeSpec) (childRules []M, entryRules []M) {
	if !nc.IsRelayEntry() {
		return nil, nil
	}

	if nc.Relay.RouteID > 0 {
		entryRules = append(entryRules, M{
			"type":        "field",
			"vlessRoute":  strconv.Itoa(nc.Relay.RouteID),
			"outboundTag": "direct",
		})
	}
	childRules = make([]M, 0, len(nc.Relay.Children))
	for _, routeID := range nc.Relay.BlockedRouteIDs {
		childRules = append(childRules, M{"type": "field", "vlessRoute": strconv.Itoa(routeID), "outboundTag": "block"})
	}
	for _, child := range nc.Relay.Children {
		if child.RouteID <= 0 || child.Tag == "" {
			continue
		}
		childRules = append(childRules, M{
			"type":        "field",
			"vlessRoute":  strconv.Itoa(child.RouteID),
			"outboundTag": child.Tag,
		})
	}
	return childRules, entryRules
}

// buildRelayLandingInbound 生成落地节点的私有内部入站。
// 该入站只有入口和落地共享的一组凭据，不包含面板用户，因此不会重复计算用户流量。
func buildRelayLandingInbound(kcfg config.KernelConfig, nc *model.NodeSpec, tc kernel.TLSCert) M {
	if !nc.IsRelayLanding() {
		return nil
	}

	listenAddr := "::"
	if nc.ListenIP != "" {
		listenAddr = nc.ListenIP
	}

	port := nc.Relay.ListenPort
	if port == 0 {
		port = nc.ServerPort
	}

	base := M{
		"tag":    relayLandingInboundTag,
		"listen": listenAddr,
		"port":   port,
		"streamSettings": M{
			"sockopt": M{
				"reusePort": true,
			},
		},
	}

	switch strings.ToLower(strings.TrimSpace(nc.Relay.Protocol)) {
	case "wireguard":
		if nc.Relay.WireGuard == nil {
			return nil
		}
		base["protocol"] = "wireguard"
		base["settings"] = relayWireGuardSettings(nc.Relay.WireGuard)
		return base
	case "shadowsocks":
		base["protocol"] = "shadowsocks"
		base["settings"] = M{
			"method":   nc.Relay.Cipher,
			"password": nc.Relay.Password,
			"network":  "tcp,udp",
		}
		return base
	case "vless":
		if nc.Relay.VLESS == nil {
			return nil
		}
		base["protocol"] = "vless"
		client := M{"id": nc.Relay.VLESS.ID}
		if nc.Flow != "" {
			client["flow"] = nc.Flow
		}
		decryption := nc.Decryption
		if decryption == "" {
			decryption = "none"
		}
		base["settings"] = M{
			"clients":    []M{client},
			"decryption": decryption,
		}
		applyStreamSettings(base, kcfg, nc, tc)
		return base
	default:
		return nil
	}
}

func buildRelayVLESSClientStream(v *model.RelayVLESSConfig) M {
	ss := buildTransportStreamSettings(v.Network, v.NetworkSettings, v.TransportAuth)

	switch v.TLS {
	case 1:
		tlsSettings := M{}
		if value := stringSetting(v.TLSSettings, "server_name"); value != "" {
			tlsSettings["serverName"] = value
		}
		if value := stringSetting(v.TLSSettings, "fingerprint"); value != "" {
			tlsSettings["fingerprint"] = value
		}
		if network, _ := model.NormalizeRelayVLESSNetwork(v.Network); network == "hysteria" {
			tlsSettings["alpn"] = []string{"h3"}
		}
		ss["security"] = "tls"
		ss["tlsSettings"] = tlsSettings
	case 2:
		fingerprint := stringSetting(v.RealitySettings, "fingerprint")
		if fingerprint == "" {
			fingerprint = "chrome"
		}
		ss["security"] = "reality"
		ss["realitySettings"] = M{
			"fingerprint": fingerprint,
			"serverName":  stringSetting(v.RealitySettings, "server_name"),
			"password":    stringSetting(v.RealitySettings, "public_key"),
			"shortId":     stringSetting(v.RealitySettings, "short_id"),
			"spiderX":     "/",
		}
	}

	return ss
}

func relayWireGuardSettings(w *model.RelayWireGuardConfig) M {
	return M{
		"secretKey": w.PrivateKey, "address": w.Address, "mtu": w.MTU, "noKernelTun": true,
		"peers": []M{{"publicKey": w.PeerPublicKey, "allowedIPs": w.AllowedIPs, "keepAlive": w.Keepalive}},
	}
}

// buildTransportStreamSettings 将面板传输参数转换为 Xray streamSettings。
// 普通入站、VLESS 中转入站和中转出站共用该函数，避免两端映射不一致。
func buildTransportStreamSettings(network string, settings map[string]any, transportAuth string) M {
	canonical, ok := model.NormalizeRelayVLESSNetwork(network)
	if !ok {
		canonical = network
		if canonical == "" {
			canonical = "tcp"
		}
	}

	ss := M{"network": canonical}
	copySettings := cloneXrayMap(settings)

	switch canonical {
	case "tcp":
		if len(copySettings) > 0 {
			ss["tcpSettings"] = copySettings
		}
	case "ws":
		ss["wsSettings"] = copySettings
	case "grpc":
		if value, exists := copySettings["service_name"]; exists {
			if _, canonicalExists := copySettings["serviceName"]; !canonicalExists {
				copySettings["serviceName"] = value
			}
			delete(copySettings, "service_name")
		}
		ss["grpcSettings"] = copySettings
	case "httpupgrade":
		ss["httpupgradeSettings"] = copySettings
	case "xhttp":
		if extra, exists := copySettings["extra"].(map[string]any); exists {
			sanitizeEmptyArrays(extra)
			if len(extra) == 0 {
				delete(copySettings, "extra")
			}
		}
		ss["xhttpSettings"] = copySettings
	case "kcp":
		ss["kcpSettings"] = copySettings
	case "hysteria":
		copySettings["version"] = 2
		copySettings["auth"] = transportAuth
		if _, exists := copySettings["udpIdleTimeout"]; !exists {
			copySettings["udpIdleTimeout"] = 60
		}
		ss["hysteriaSettings"] = copySettings
	}

	return ss
}

func cloneXrayMap(src map[string]any) M {
	if len(src) == 0 {
		return M{}
	}
	out := make(M, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneXrayMap(typed)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			out[key] = items
		default:
			out[key] = value
		}
	}
	return out
}

func stringSetting(settings map[string]any, key string) string {
	value, _ := settings[key].(string)
	return value
}

// GetRelayTraffic 返回跨实例累计的逻辑节点流量，旧中转配置排空后仍保留已结算值。
func (x *Xray) GetRelayTraffic(_ context.Context) (map[int][2]int64, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.collectStatsLocked()
	return copyTraffic(x.cumRelayTraffic), nil
}

// GetRelayUserTraffic 返回跨实例累计的用户-逻辑节点流量，不受当前用户集和节点角色影响。
func (x *Xray) GetRelayUserTraffic(_ context.Context) (map[int]map[int][2]int64, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.collectStatsLocked()
	out := make(map[int]map[int][2]int64)
	for uid, nodes := range x.cumRelayUserTraffic {
		if copied := copyTraffic(nodes); copied != nil {
			out[uid] = copied
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// relayRouteNodes 返回中转入口路由编号到落地节点的映射，与中转流量计数使用同一组编号。
// 非入口节点返回 nil，调度器不按实际节点登记来源。
func relayRouteNodes(nc *model.NodeSpec) map[xnet.Port]int {
	if !nc.IsRelayEntry() {
		return nil
	}
	routes := make(map[xnet.Port]int, len(nc.Relay.Children))
	for _, child := range nc.Relay.Children {
		if child.RouteID > 0 && child.NodeID > 0 {
			routes[xnet.Port(child.RouteID)] = child.NodeID
		}
	}
	return routes
}

// GetRelayUserAlive 合并当前实例和排空中旧实例按实际出网节点拆分的在线来源。
func (x *Xray) GetRelayUserAlive(_ context.Context) (map[int]map[int]map[string]bool, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	out := make(map[int]map[int]map[string]bool)
	merge := func(ld *LimitDispatcher) {
		if ld == nil {
			return
		}
		for uid, nodes := range ld.RelayUserAlive() {
			if out[uid] == nil {
				out[uid] = make(map[int]map[string]bool, len(nodes))
			}
			for node, ips := range nodes {
				if out[uid][node] == nil {
					out[uid][node] = make(map[string]bool, len(ips))
				}
				for ip := range ips {
					out[uid][node][ip] = true
				}
			}
		}
	}
	merge(x.limitDispatcher)
	for _, previous := range x.retired {
		merge(previous.dispatcher)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

var _ kernel.RelayUserAliveReader = (*Xray)(nil)

// GetConnectionSnapshot 合并当前实例与排空中的旧实例，避免热重载后漏算旧连接。
func (x *Xray) GetConnectionSnapshot(_ context.Context) (map[int]int, map[int]map[int]int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	counts := make(map[int]int)
	relay := make(map[int]map[int]int)
	merge := func(ld *LimitDispatcher) {
		if ld == nil {
			return
		}
		users, nodes := ld.ConnectionSnapshot()
		for uid, count := range users {
			counts[uid] += count
		}
		for uid, entries := range nodes {
			if relay[uid] == nil {
				relay[uid] = make(map[int]int)
			}
			for node, count := range entries {
				relay[uid][node] += count
			}
		}
	}
	merge(x.limitDispatcher)
	for _, previous := range x.retired {
		merge(previous.dispatcher)
	}
	return counts, relay, nil
}

var _ kernel.ConnectionSnapshotReader = (*Xray)(nil)
