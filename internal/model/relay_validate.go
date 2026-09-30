package model

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

// relayTransitCiphers lists the Shadowsocks methods allowed on the internal
// entry-to-landing link. It must stay in sync with the panel's allow list.
var relayTransitCiphers = map[string]bool{
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
	"aes-128-gcm":                   true,
	"aes-192-gcm":                   true,
	"aes-256-gcm":                   true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-ietf-poly1305":       true,
}

// IsRelayTransitCipher reports whether the method is usable for the internal link.
func IsRelayTransitCipher(cipher string) bool {
	return relayTransitCiphers[strings.ToLower(strings.TrimSpace(cipher))]
}

// IsRelaySS2022Cipher reports whether the method is a Shadowsocks 2022 method,
// which uses a base64 pre-shared key instead of a plain password.
func IsRelaySS2022Cipher(cipher string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(cipher)), "2022-blake3-")
}

// validateRelay 校验中转拓扑，并按入口或落地实际使用的内核检查协议能力。
func validateRelay(n *NodeSpec, kernelType string, availableTags map[string]struct{}) error {
	if n == nil || n.Relay == nil {
		return nil
	}

	switch n.Relay.Mode {
	case "":
		return nil
	case relayModeEntry:
		return validateRelayEntry(n, kernelType, availableTags)
	case relayModeLanding:
		return validateRelayLanding(n, kernelType)
	default:
		return fmt.Errorf("unsupported relay mode %q", n.Relay.Mode)
	}
}

func validateRelayEntry(n *NodeSpec, kernelType string, availableTags map[string]struct{}) error {
	if kernelType != "xray" && kernelType != "singbox" {
		return fmt.Errorf("unsupported relay entry kernel %s", kernelType)
	}
	switch strings.ToLower(strings.TrimSpace(n.Protocol)) {
	case "vless":
		if err := validateRelayEntryVLESS(n); err != nil {
			return err
		}
		if kernelType == "singbox" {
			if err := validateSingBoxRelayVLESS(n.Network, n.Flow, n.Decryption, n.TLS, n.NetworkSettings); err != nil {
				return err
			}
		}
	case "hysteria":
		if n.Version != 2 {
			return fmt.Errorf("relay entry requires hysteria version 2, got %d", n.Version)
		}
		if n.Obfs != "" && n.Obfs != "salamander" {
			return fmt.Errorf("relay entry: unsupported hysteria2 obfuscation %q", n.Obfs)
		}
		if n.Obfs == "salamander" && len(n.ObfsPassword) < 4 {
			return fmt.Errorf("relay entry: salamander password must contain at least 4 bytes")
		}
		if ech, ok := n.TLSSettings["ech"].(map[string]any); ok && ech["enabled"] == true {
			if strings.TrimSpace(anyString(ech["key"])) == "" && strings.TrimSpace(anyString(ech["key_path"])) == "" {
				return fmt.Errorf("relay entry: hysteria2 ECH requires key or key_path")
			}
		}
	default:
		return fmt.Errorf("relay entry requires a vless or hysteria2 inbound, got %q", n.Protocol)
	}
	if err := validateRouteID(n.Relay.RouteID, "relay entry route_id"); err != nil {
		return err
	}

	seenTags := make(map[string]struct{}, len(n.Relay.Children))
	seenRoutes := make(map[int]struct{}, len(n.Relay.Children)+1)
	seenRoutes[n.Relay.RouteID] = struct{}{}
	for _, routeID := range n.Relay.BlockedRouteIDs {
		if err := validateRouteID(routeID, "blocked relay route"); err != nil {
			return err
		}
		if _, exists := seenRoutes[routeID]; exists {
			return fmt.Errorf("duplicate blocked relay route")
		}
		seenRoutes[routeID] = struct{}{}
	}

	for i := range n.Relay.Children {
		child := &n.Relay.Children[i]
		if child.NodeID <= 0 {
			return fmt.Errorf("relay child %d: node_id must be positive", i)
		}
		tag := strings.ToLower(strings.TrimSpace(child.Tag))
		if tag == "" {
			return fmt.Errorf("relay child %d: tag must not be empty", child.NodeID)
		}
		if tag == "direct" || tag == "block" {
			return fmt.Errorf("relay child %d: tag %q is reserved", child.NodeID, tag)
		}
		if _, dup := seenTags[tag]; dup {
			return fmt.Errorf("relay child %d: duplicate tag %q", child.NodeID, tag)
		}
		if _, clash := availableTags[tag]; clash {
			return fmt.Errorf("relay child %d: tag %q collides with a custom outbound", child.NodeID, tag)
		}
		seenTags[tag] = struct{}{}

		if err := validateRouteID(child.RouteID, fmt.Sprintf("relay child %d route_id", child.NodeID)); err != nil {
			return err
		}
		if _, dup := seenRoutes[child.RouteID]; dup {
			return fmt.Errorf("relay child %d: duplicate route_id %d", child.NodeID, child.RouteID)
		}
		seenRoutes[child.RouteID] = struct{}{}

		if strings.TrimSpace(child.Address) == "" {
			return fmt.Errorf("relay child %d: address must not be empty", child.NodeID)
		}
		if child.Port <= 0 || child.Port > 65535 {
			return fmt.Errorf("relay child %d: invalid port %d", child.NodeID, child.Port)
		}

		switch strings.ToLower(strings.TrimSpace(child.Protocol)) {
		case "wireguard":
			if err := validateRelayWireGuard(child.WireGuard); err != nil {
				return fmt.Errorf("relay child %d: %w", child.NodeID, err)
			}
		case "shadowsocks":
			if !IsRelayTransitCipher(child.Cipher) {
				return fmt.Errorf("relay child %d: unsupported cipher %q", child.NodeID, child.Cipher)
			}
			if strings.TrimSpace(child.Password) == "" {
				return fmt.Errorf("relay child %d: password must not be empty", child.NodeID)
			}
		case "vless":
			if err := validateRelayVLESS(child.VLESS, fmt.Sprintf("relay child %d", child.NodeID)); err != nil {
				return err
			}
			if kernelType == "singbox" {
				v := child.VLESS
				if err := validateSingBoxRelayVLESS(v.Network, v.Flow, v.Encryption, v.TLS, v.NetworkSettings); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("relay child %d: unsupported transit protocol %q", child.NodeID, child.Protocol)
		}
	}

	return nil
}

func validateRelayEntryVLESS(n *NodeSpec) error {
	network := n.Network
	if strings.TrimSpace(network) == "" {
		network = "tcp"
	}
	canonical, ok := NormalizeRelayVLESSNetwork(network)
	if !ok {
		return fmt.Errorf("relay entry: unsupported vless transport %q", n.Network)
	}
	if n.TLS < 0 || n.TLS > 2 {
		return fmt.Errorf("relay entry: invalid vless tls mode %d", n.TLS)
	}
	if n.TLS == 2 && canonical != "tcp" && canonical != "xhttp" && canonical != "grpc" {
		return fmt.Errorf("relay entry: reality only supports raw, xhttp and grpc")
	}
	if canonical == "hysteria" && n.TLS != 1 {
		return fmt.Errorf("relay entry: hysteria transport requires tls")
	}
	if n.Flow != "" && n.Flow != "xtls-rprx-vision" {
		return fmt.Errorf("relay entry: unsupported vless flow %q", n.Flow)
	}
	return nil
}

func validateRelayLanding(n *NodeSpec, kernelType string) error {
	if kernelType != "xray" && kernelType != "singbox" {
		return fmt.Errorf("unsupported relay landing kernel %s", kernelType)
	}
	port := n.Relay.ListenPort
	if port == 0 {
		port = n.ServerPort
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("relay landing: invalid listen port %d", port)
	}

	switch strings.ToLower(strings.TrimSpace(n.Relay.Protocol)) {
	case "wireguard":
		return validateRelayWireGuard(n.Relay.WireGuard)
	case "shadowsocks":
		if !IsRelayTransitCipher(n.Relay.Cipher) {
			return fmt.Errorf("relay landing: unsupported cipher %q", n.Relay.Cipher)
		}
		if strings.TrimSpace(n.Relay.Password) == "" {
			return fmt.Errorf("relay landing: password must not be empty")
		}
	case "vless":
		landingVLESS := &RelayVLESSConfig{
			ID:              "",
			Network:         n.Network,
			NetworkSettings: n.NetworkSettings,
			TLS:             n.TLS,
			Flow:            n.Flow,
			Encryption:      "none",
			TLSSettings:     n.TLSSettings,
		}
		if n.Relay.VLESS != nil {
			landingVLESS.ID = n.Relay.VLESS.ID
			landingVLESS.TransportAuth = n.Relay.VLESS.TransportAuth
		}
		if n.TLS == 2 {
			// 服务端 Reality 参数位于 NodeSpec.TLSSettings；这里只验证组合，
			// 公钥等客户端字段不会被要求出现在落地配置中。
			landingVLESS.RealitySettings = map[string]any{
				"server_name": "server-side",
				"public_key":  "server-side",
				"fingerprint": "chrome",
			}
		}
		if err := validateRelayVLESS(landingVLESS, "relay landing"); err != nil {
			return err
		}
		decryption := strings.TrimSpace(n.Decryption)
		if decryption == "" {
			decryption = "none"
		}
		if decryption != "none" && !validVLESSEncryption(decryption, true) {
			return fmt.Errorf("relay landing: invalid vless decryption")
		}
		if kernelType == "singbox" {
			if err := validateSingBoxRelayVLESS(n.Network, n.Flow, decryption, n.TLS, n.NetworkSettings); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("relay landing: unsupported transit protocol %q", n.Relay.Protocol)
	}
	return nil
}

func validateSingBoxRelayVLESS(network, flow, encryption string, tlsMode int, settings map[string]any) error {
	if network == "" {
		network = "tcp"
	}
	canonical, _ := NormalizeRelayVLESSNetwork(network)
	switch canonical {
	case "tcp", "ws", "grpc", "httpupgrade":
	default:
		return fmt.Errorf("sing-box relay does not support vless transport %q", network)
	}
	if encryption != "" && encryption != "none" {
		return fmt.Errorf("sing-box relay does not support VLESS Encryption")
	}
	if flow == "xtls-rprx-vision" && (canonical != "tcp" || tlsMode == 0) {
		return fmt.Errorf("sing-box relay Vision requires tcp with TLS or Reality")
	}
	if header, ok := settings["header"].(map[string]any); canonical == "tcp" && ok {
		if value := anyString(header["type"]); value != "" && value != "none" {
			return fmt.Errorf("sing-box relay does not support tcp header camouflage")
		}
	}
	return nil
}

var relayVLESSNetworks = map[string]string{
	"tcp": "tcp", "raw": "tcp",
	"ws": "ws", "websocket": "ws",
	"grpc":  "grpc",
	"xhttp": "xhttp", "splithttp": "xhttp",
	"httpupgrade": "httpupgrade",
	"kcp":         "kcp", "mkcp": "kcp",
	"hysteria": "hysteria",
}

// NormalizeRelayVLESSNetwork 返回中转构建器使用的标准传输名称。
// 当前固定版本的 Xray Core 已移除 H2/HTTP，因此这里不提供兼容别名。
func NormalizeRelayVLESSNetwork(network string) (string, bool) {
	canonical, ok := relayVLESSNetworks[strings.ToLower(strings.TrimSpace(network))]
	return canonical, ok
}

func validateRelayVLESS(v *RelayVLESSConfig, field string) error {
	if v == nil {
		return fmt.Errorf("%s: vless settings must not be empty", field)
	}
	if !validRelayUUID(v.ID) {
		return fmt.Errorf("%s: invalid vless id", field)
	}
	networkName := v.Network
	if strings.TrimSpace(networkName) == "" {
		networkName = "tcp"
	}
	network, ok := NormalizeRelayVLESSNetwork(networkName)
	if !ok {
		return fmt.Errorf("%s: unsupported vless transport %q", field, v.Network)
	}
	if v.TLS < 0 || v.TLS > 2 {
		return fmt.Errorf("%s: invalid vless tls mode %d", field, v.TLS)
	}
	if v.TLS == 2 && network != "tcp" && network != "xhttp" && network != "grpc" {
		return fmt.Errorf("%s: reality only supports raw, xhttp and grpc", field)
	}
	if network == "hysteria" {
		if v.TLS != 1 {
			return fmt.Errorf("%s: hysteria transport requires tls", field)
		}
		if strings.TrimSpace(v.TransportAuth) == "" {
			return fmt.Errorf("%s: hysteria transport auth must not be empty", field)
		}
	}
	if v.Flow != "" && v.Flow != "xtls-rprx-vision" {
		return fmt.Errorf("%s: unsupported vless flow %q", field, v.Flow)
	}
	encryption := strings.TrimSpace(v.Encryption)
	if encryption == "" {
		return fmt.Errorf("%s: vless encryption must be explicit", field)
	}
	if encryption != "none" && !validVLESSEncryption(encryption, false) {
		return fmt.Errorf("%s: invalid vless encryption", field)
	}
	if v.TLS == 2 {
		for _, key := range []string{"server_name", "public_key", "fingerprint"} {
			if strings.TrimSpace(anyString(v.RealitySettings[key])) == "" {
				return fmt.Errorf("%s: reality %s must not be empty", field, key)
			}
		}
	}
	if network == "kcp" {
		if _, exists := v.NetworkSettings["header"]; exists {
			return fmt.Errorf("%s: mkcp header was removed by the pinned xray core", field)
		}
		if _, exists := v.NetworkSettings["seed"]; exists {
			return fmt.Errorf("%s: mkcp seed was removed by the pinned xray core", field)
		}
	}
	return nil
}

func validRelayUUID(value string) bool {
	raw := strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(raw) != 32 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func normalizeVLESSFlow(value string) string {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "none") {
		return ""
	}
	return value
}

func validVLESSEncryption(value string, decryption bool) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) < 4 || parts[0] != "mlkem768x25519plus" {
		return false
	}
	switch parts[1] {
	case "native", "xorpub", "random":
	default:
		return false
	}
	if decryption {
		if !validVLESSSeconds(parts[2]) {
			return false
		}
	} else if parts[2] != "0rtt" && parts[2] != "1rtt" {
		return false
	}

	hasKey := false
	for _, part := range parts[3:] {
		// 长度不足 20 的段由 Xray 解释为可选 padding，其余段必须是密钥。
		if len(part) < 20 {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			return false
		}
		if decryption {
			if len(decoded) != 32 && len(decoded) != 64 {
				return false
			}
		} else if len(decoded) != 32 && len(decoded) != 1184 {
			return false
		}
		hasKey = true
	}
	return hasKey
}

func validVLESSSeconds(value string) bool {
	if !strings.HasSuffix(value, "s") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(value, "s"), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func validateRouteID(routeID int, field string) error {
	// 0 is unusable: Xray's port-list parser drops a bare numeric zero.
	if routeID < 1 || routeID > 65535 {
		return fmt.Errorf("%s must be within 1-65535, got %d", field, routeID)
	}
	return nil
}

func validateRelayWireGuard(w *RelayWireGuardConfig) error {
	if w == nil {
		return fmt.Errorf("wireguard settings are required")
	}
	private, err := base64.StdEncoding.DecodeString(w.PrivateKey)
	if err != nil || len(private) != 32 {
		return fmt.Errorf("invalid wireguard private key")
	}
	peer, err := base64.StdEncoding.DecodeString(w.PeerPublicKey)
	if err != nil || len(peer) != 32 {
		return fmt.Errorf("invalid wireguard peer public key")
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return fmt.Errorf("invalid wireguard private key")
	}
	public, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return fmt.Errorf("invalid wireguard peer public key")
	}
	if _, err = key.ECDH(public); err != nil || key.PublicKey().Equal(public) {
		return fmt.Errorf("invalid wireguard peer public key")
	}
	if w.MTU < 1280 || w.MTU > 1420 || w.Keepalive < 0 || w.Keepalive > 65535 {
		return fmt.Errorf("invalid wireguard MTU or keepalive")
	}
	if len(w.Address) == 0 || len(w.AllowedIPs) == 0 {
		return fmt.Errorf("wireguard addresses and allowed IPs are required")
	}
	for _, value := range append(append([]string(nil), w.Address...), w.AllowedIPs...) {
		if _, err := netip.ParsePrefix(value); err != nil {
			return fmt.Errorf("invalid wireguard address prefix")
		}
	}
	return nil
}
