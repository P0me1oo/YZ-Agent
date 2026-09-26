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

func (s *SingBox) GetRelayUserTraffic(context.Context) (map[int]map[int][2]int64, error) {
	return s.traffic.relayUserSnapshot(), nil
}

var _ kernel.RelayTrafficReader = (*SingBox)(nil)
var _ kernel.RelayUserTrafficReader = (*SingBox)(nil)

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
