package singbox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
)

const relayLandingInboundTag = "relay-in"

type relayIdentity struct {
	UserID int
	UUID   string
}

// 线路身份只用于核心内部选路，日志中的名称不包含可用于认证的原始凭据。
func relayUserName(user model.UserSpec, routeID int) string {
	digest := sha256.Sum256([]byte(user.UUID))
	return fmt.Sprintf("relay-u%d-r%d-%x", user.ID, routeID, digest[:16])
}

// 与面板 applyVlessRoute 保持一致；HY2 必须保留原始字符串的大小写。
func relayCredential(user model.UserSpec, routeID int) string {
	if len(user.UUID) != 36 {
		return ""
	}
	return user.UUID[:14] + fmt.Sprintf("%04x", routeID) + user.UUID[18:]
}

func relayRouteIDs(nc *model.NodeSpec) []int {
	if !nc.IsRelayEntry() {
		return nil
	}
	ids := make([]int, 0, len(nc.Relay.Children)+1)
	ids = append(ids, nc.Relay.RouteID)
	for _, child := range nc.Relay.Children {
		ids = append(ids, child.RouteID)
	}
	return ids
}

// 拒绝覆盖了线路字节后发生身份碰撞的用户，不能让后加入的身份覆盖已有用户。
func validateRelayUsers(nc *model.NodeSpec, users []model.UserSpec) error {
	if !nc.IsRelayEntry() {
		return nil
	}
	seen := make(map[string]int, len(users))
	ids := make(map[int]bool, len(users))
	for _, user := range users {
		if _, err := uuid.FromString(user.UUID); err != nil || len(user.UUID) != 36 || user.ID <= 0 {
			return fmt.Errorf("invalid relay user identity for user %d", user.ID)
		}
		if ids[user.ID] {
			return fmt.Errorf("duplicate relay user ID %d", user.ID)
		}
		ids[user.ID] = true
		identity := strings.ToLower(relayCredential(user, 0))
		if previous, exists := seen[identity]; exists {
			return fmt.Errorf("relay identities collide for users %d and %d", previous, user.ID)
		}
		seen[identity] = user.ID
	}
	return nil
}

func configureRelayUsers(inbound M, nc *model.NodeSpec, users []model.UserSpec) {
	if inbound == nil || !nc.IsRelayEntry() {
		return
	}
	routes := relayRouteIDs(nc)
	aliases := make([]M, 0, len(users)*len(routes))
	for _, user := range users {
		for _, routeID := range routes {
			alias := M{"name": relayUserName(user, routeID)}
			if nc.Protocol == "vless" {
				alias["uuid"] = relayCredential(user, routeID)
				if nc.Flow != "" {
					alias["flow"] = nc.Flow
				}
			} else {
				alias["password"] = relayCredential(user, routeID)
			}
			aliases = append(aliases, alias)
		}
	}
	inbound["users"] = aliases
}

func buildRelayRoutingRules(nc *model.NodeSpec, users []model.UserSpec) []M {
	if !nc.IsRelayEntry() || len(users) == 0 {
		return nil
	}
	rules := make([]M, 0, len(nc.Relay.Children)+1)
	add := func(routeID int, tag string) {
		names := make([]string, 0, len(users))
		for _, user := range users {
			names = append(names, relayUserName(user, routeID))
		}
		rules = append(rules, M{"auth_user": names, "action": "route", "outbound": tag})
	}
	add(nc.Relay.RouteID, "direct")
	for _, child := range nc.Relay.Children {
		add(child.RouteID, child.Tag)
	}
	return rules
}

func buildRelayOutbounds(nc *model.NodeSpec) []M {
	if !nc.IsRelayEntry() {
		return nil
	}
	outbounds := make([]M, 0, len(nc.Relay.Children))
	for _, child := range nc.Relay.Children {
		protocol := strings.ToLower(strings.TrimSpace(child.Protocol))
		if protocol == "wireguard" {
			continue
		}
		outbound := M{"tag": child.Tag, "type": protocol, "server": child.Address, "server_port": child.Port}
		if protocol == "shadowsocks" {
			outbound["method"], outbound["password"] = child.Cipher, child.Password
		} else if child.VLESS != nil {
			v := child.VLESS
			outbound["uuid"] = v.ID
			outbound["packet_encoding"] = "xudp"
			if v.Flow != "" {
				outbound["flow"] = v.Flow
			}
			network, _ := model.NormalizeRelayVLESSNetwork(v.Network)
			applyTransport(outbound, &model.NodeSpec{Network: network, NetworkSettings: v.NetworkSettings})
			if v.TLS != 0 {
				settings := v.TLSSettings
				if v.TLS == 2 {
					settings = v.RealitySettings
				}
				tls := M{"enabled": true}
				if name, _ := settings["server_name"].(string); name != "" {
					tls["server_name"] = name
				}
				if fingerprint, _ := settings["fingerprint"].(string); fingerprint != "" {
					tls["utls"] = M{"enabled": true, "fingerprint": fingerprint}
				}
				if v.TLS == 2 {
					tls["reality"] = M{"enabled": true, "public_key": settings["public_key"], "short_id": settings["short_id"]}
					if tls["utls"] == nil {
						tls["utls"] = M{"enabled": true, "fingerprint": "chrome"}
					}
				}
				outbound["tls"] = tls
			}
		}
		outbounds = append(outbounds, outbound)
	}
	return outbounds
}

// 落地只接受内部凭据；无面板用户时也必须正常监听，且不能重复上报用户流量。
func buildRelayLandingInbound(nc *model.NodeSpec, tc kernel.TLSCert) M {
	if nc.Relay.Protocol == "wireguard" {
		return nil
	}
	landing := *nc
	landing.Relay = nil
	landing.Protocol = strings.ToLower(strings.TrimSpace(nc.Relay.Protocol))
	if nc.Relay.ListenPort != 0 {
		landing.ServerPort = nc.Relay.ListenPort
	}
	if landing.ListenIP == "" {
		landing.ListenIP = "::"
	}
	if landing.Protocol == "shadowsocks" {
		return M{"type": "shadowsocks", "tag": relayLandingInboundTag, "listen": landing.ListenIP,
			"listen_port": landing.ServerPort, "method": nc.Relay.Cipher, "password": nc.Relay.Password}
	}
	if nc.Relay.VLESS == nil {
		return nil
	}
	landing.Network, _ = model.NormalizeRelayVLESSNetwork(landing.Network)
	inbound := buildInbound(&landing, []model.UserSpec{{UUID: nc.Relay.VLESS.ID}}, tc)
	inbound["tag"] = relayLandingInboundTag
	for _, user := range inbound["users"].([]M) {
		user["name"] = "relay-transit"
	}
	return inbound
}

func (s *SingBox) GetRelayTraffic(context.Context) (map[int][2]int64, error) {
	return s.traffic.relaySnapshot(), nil
}

// WireGuard 使用端点而非已移除的旧版出站；用户态收发不创建系统网卡。
func buildRelayWireGuardEndpoints(nc *model.NodeSpec) []M {
	endpoint := func(tag string, w *model.RelayWireGuardConfig) M {
		return M{"type": "wireguard", "tag": tag, "system": false,
			"private_key": w.PrivateKey, "address": w.Address, "mtu": w.MTU,
			"peers": []M{{"public_key": w.PeerPublicKey, "allowed_ips": w.AllowedIPs,
				"persistent_keepalive_interval": w.Keepalive}}}
	}
	var endpoints []M
	if nc.IsRelayLanding() && nc.Relay.Protocol == "wireguard" && nc.Relay.WireGuard != nil {
		e := endpoint(relayLandingInboundTag, nc.Relay.WireGuard)
		port := nc.Relay.ListenPort
		if port == 0 {
			port = nc.ServerPort
		}
		e["listen_port"] = port
		endpoints = append(endpoints, e)
	}
	if nc.IsRelayEntry() {
		for _, child := range nc.Relay.Children {
			if child.Protocol != "wireguard" || child.WireGuard == nil {
				continue
			}
			e := endpoint(child.Tag, child.WireGuard)
			peer := e["peers"].([]M)[0]
			peer["address"], peer["port"] = child.Address, child.Port
			endpoints = append(endpoints, e)
		}
	}
	return endpoints
}

func (s *SingBox) GetRelayUserTraffic(context.Context) (map[int]map[int][2]int64, error) {
	return s.traffic.relayUserSnapshot(), nil
}

// GetRelayUserAlive 返回中转入口按实际出网节点拆分的在线来源。
func (s *SingBox) GetRelayUserAlive(context.Context) (map[int]map[int]map[string]bool, error) {
	ct := s.connTrackerSafe()
	if ct == nil {
		return nil, nil
	}
	return ct.relaySourceSnapshot(), nil
}

var _ kernel.RelayTrafficReader = (*SingBox)(nil)
var _ kernel.RelayUserTrafficReader = (*SingBox)(nil)
var _ kernel.RelayUserAliveReader = (*SingBox)(nil)

// GetConnectionSnapshot 读取用户连接计数，并按入口实际出网节点拆分。
func (s *SingBox) GetConnectionSnapshot(context.Context) (map[int]int, map[int]map[int]int, error) {
	ct := s.connTrackerSafe()
	if ct == nil {
		return nil, nil, nil
	}
	counts := make(map[int]int)
	ct.usersMu.RLock()
	for uid, stats := range ct.users {
		if n := stats.currentConns(); n > 0 {
			counts[uid] = n
		}
	}
	ct.usersMu.RUnlock()
	relay := make(map[int]map[int]int)
	ct.relaySourceMu.Lock()
	for key, ips := range ct.relaySources {
		for _, n := range ips {
			if n > 0 {
				if relay[key.userID] == nil {
					relay[key.userID] = make(map[int]int)
				}
				relay[key.userID][key.nodeID] += n
			}
		}
	}
	ct.relaySourceMu.Unlock()
	return counts, relay, nil
}

var _ kernel.ConnectionSnapshotReader = (*SingBox)(nil)

// relaySourceKey 标识中转入口上的用户和实际出网节点，节点 0 表示入口直连。
type relaySourceKey struct {
	userID int
	nodeID int
}

// trackRelaySource 在中转入口登记本连接的来源 IP，返回关闭时的回收函数；其他节点返回 nil。
// 出网节点取实际选中的出站，与中转流量归属使用同一判定。
func (t *ConnTracker) trackRelaySource(userID int, outbound adapter.Outbound, sourceIP string) func() {
	if userID <= 0 {
		return nil
	}
	t.usersMu.RLock()
	entry := t.relayEntry
	nodeID := 0
	if outbound != nil {
		nodeID = t.relayNodes[outbound.Tag()]
	}
	t.usersMu.RUnlock()
	if !entry {
		return nil
	}

	key := relaySourceKey{userID: userID, nodeID: nodeID}
	t.relaySourceMu.Lock()
	if t.relaySources == nil {
		t.relaySources = make(map[relaySourceKey]map[string]int)
	}
	if t.relaySources[key] == nil {
		t.relaySources[key] = make(map[string]int)
	}
	t.relaySources[key][sourceIP]++
	t.relaySourceMu.Unlock()

	return func() {
		t.relaySourceMu.Lock()
		defer t.relaySourceMu.Unlock()
		ips := t.relaySources[key]
		if ips == nil {
			return
		}
		if ips[sourceIP]--; ips[sourceIP] <= 0 {
			delete(ips, sourceIP)
		}
		if len(ips) == 0 {
			delete(t.relaySources, key)
		}
	}
}

func (t *ConnTracker) relaySourceSnapshot() map[int]map[int]map[string]bool {
	t.relaySourceMu.Lock()
	defer t.relaySourceMu.Unlock()
	if len(t.relaySources) == 0 {
		return nil
	}
	out := make(map[int]map[int]map[string]bool)
	for key, ips := range t.relaySources {
		if len(ips) == 0 {
			continue
		}
		if out[key.userID] == nil {
			out[key.userID] = make(map[int]map[string]bool)
		}
		set := make(map[string]bool, len(ips))
		for ip := range ips {
			set[ip] = true
		}
		out[key.userID][key.nodeID] = set
	}
	return out
}

func (t *ConnTracker) setNodeUsers(nc *model.NodeSpec, users []model.UserSpec) {
	t.replaceNodeUsers(nc, users)
	t.RefreshSpeedLimits()
}

func (t *ConnTracker) replaceNodeUsers(nc *model.NodeSpec, users []model.UserSpec) {
	identities := make(map[string]relayIdentity)
	userMap := buildUserMap(users)
	nodes := make(map[string]int)
	if nc.IsRelayLanding() {
		userMap = make(map[string]int)
	} else if nc.IsRelayEntry() {
		userMap = make(map[string]int)
		routes := relayRouteIDs(nc)
		for _, user := range users {
			for _, route := range routes {
				name := relayUserName(user, route)
				userMap[name] = user.ID
				identities[name] = relayIdentity{UserID: user.ID, UUID: user.UUID}
			}
		}
		for _, child := range nc.Relay.Children {
			nodes[child.Tag] = child.NodeID
		}
	}
	t.usersMu.Lock()
	defer t.usersMu.Unlock()
	t.uuidMap, t.identities, t.relayNodes = userMap, identities, nodes
	t.relayEntry = nc.IsRelayEntry()
	for _, uid := range userMap {
		if t.users[uid] == nil {
			t.users[uid] = &userStats{userTraffic: t.traffic.user(uid), ips: make(map[string]int)}
		}
	}
}

// 使用实际选中的出站归属流量；管理员覆盖选路时不能计入原计划的落地。
func (t *ConnTracker) resolveUser(name string, outbound adapter.Outbound) (string, int, *userStats, relayCounters, bool) {
	t.usersMu.RLock()
	uid := t.uuidMap[name]
	user := t.users[uid]
	canonical := name
	if identity, ok := t.identities[name]; ok {
		canonical = identity.UUID
	}
	nodeID := 0
	if outbound != nil {
		nodeID = t.relayNodes[outbound.Tag()]
	}
	// HY2 旧 QUIC 会话保留认证时的名称；删除或轮换后的新请求必须拒绝，不能回退到默认出站。
	allowed := !t.relayEntry || uid > 0
	t.usersMu.RUnlock()
	return canonical, uid, user, t.traffic.relayCounters(uid, nodeID), allowed
}
