package xray

import (
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	xrayTLS "github.com/xtls/xray-core/transport/internet/tls"
)

// M is a shorthand for building JSON-like maps
type M = map[string]interface{}

func buildConfig(kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	var outbounds []M
	tags := make(map[string]bool)

	// Panel-defined custom outbounds (structured, converted to Xray native)
	for _, co := range nc.CustomOutbounds {
		outbounds = append(outbounds, outboundConfigToXray(co))
		tags[strings.ToLower(co.Tag)] = true
	}

	// Static outbounds from local config file
	for _, co := range kcfg.CustomOutbound {
		tag, _ := co["tag"].(string)
		if tag != "" {
			tags[strings.ToLower(tag)] = true
		}
		outbounds = append(outbounds, M(co))
	}

	// Internal transit outbounds, one per logical node behind this entry.
	for _, ro := range buildRelayOutbounds(nc) {
		if tag, _ := ro["tag"].(string); tag != "" {
			tags[strings.ToLower(tag)] = true
		}
		outbounds = append(outbounds, ro)
	}

	// Add default outbounds only if not already defined (Issue #1: Panel priority)
	if !tags["direct"] {
		outbounds = append([]M{{"protocol": "freedom", "tag": "direct"}}, outbounds...)
	}
	if !tags["block"] {
		// block is often added after direct but before others for safety
		outbounds = append(outbounds, M{"protocol": "blackhole", "tag": "block"})
	}

	cfg := M{
		"log": M{
			"loglevel": xrayLogLevel(kcfg.LogLevel),
			"error":    "",
			"access":   "",
		},
		"stats": M{},
		"policy": M{
			"levels": M{
				"0": M{
					"statsUserUplink":   true,
					"statsUserDownlink": true,
				},
			},
			"system": M{
				"statsInboundUplink":    true,
				"statsInboundDownlink":  true,
				"statsOutboundUplink":   true,
				"statsOutboundDownlink": true,
			},
		},
		"outbounds": outbounds,
	}

	inbound := buildInbound(kcfg, nc, users, tc)
	var guardRules []M
	if inbound != nil {
		inbounds := []M{inbound}
		// REALITY 防盗用：把回源改接到只监听本机的专用入口，由它按白名单放行。
		if guard := applyRealityGuard(nc, inbound); guard != nil {
			inbounds = append(inbounds, guard.inbound)
			guardRules = guard.rules
		}
		cfg["inbounds"] = inbounds
	} else {
		nlog.Core().Warn("xray: unsupported protocol, no inbound configured — node will not accept connections",
			"protocol", nc.Protocol,
			"supported", "vmess, vless, trojan, shadowsocks, hysteria, socks, http")
	}

	// Merge panel routes and static config routes
	cfg["routing"] = buildRouting(nc.Routes, nc.CustomRouteRules, mergeRouteList(nc.CustomRoutes, kcfg.CustomRoute), buildRelayRoutingRules(nc), guardRules)

	mergeCustomXray(cfg, kcfg)
	return cfg
}

// outboundConfigToXray converts a structured OutboundConfig (from the panel)
// into an Xray outbound object. Xray uses a nested layout where protocol-
// specific fields go inside a "settings" key, and chain proxying uses "proxySettings".
func outboundConfigToXray(oc model.OutboundConfig) M {
	protocol := oc.Protocol
	switch {
	case model.IsDirectOutbound(protocol):
		protocol = "freedom"
	case model.IsBlockOutbound(protocol):
		protocol = "blackhole"
	}

	m := M{
		"protocol": protocol,
		"tag":      oc.Tag,
	}

	// sendThrough 绑定出站源地址，在 xray 里是 outbound 顶层字段而不是 settings 的一项，
	// 因此从 settings 里提升上来；settings 是共享数据，提升时复制一份再删。
	settings := oc.Settings
	for _, key := range model.SendThroughKeys {
		value, ok := settings[key]
		if !ok {
			continue
		}
		if text, isText := value.(string); !isText || strings.TrimSpace(text) == "" {
			continue
		}
		m["sendThrough"] = value
		settings = cloneWithout(settings, model.SendThroughKeys)
		break
	}

	if len(settings) > 0 {
		m["settings"] = settings
	}
	if oc.ProxyTag != "" {
		m["proxySettings"] = M{"tag": oc.ProxyTag}
	}
	return m
}

// cloneWithout 复制 settings 并去掉指定键，避免改动调用方持有的 map。
func cloneWithout(settings map[string]any, drop []string) map[string]any {
	out := make(map[string]any, len(settings))
	for k, v := range settings {
		out[k] = v
	}
	for _, k := range drop {
		delete(out, k)
	}
	return out
}

func mergeRouteList(a, b []map[string]any) []map[string]any {
	res := make([]map[string]any, 0, len(a)+len(b))
	res = append(res, a...)
	res = append(res, b...)
	return res
}

// mergeCustomXray deep-merges a custom Xray config file into the generated config.
// Merge strategy (compatible with V2bX/XrayR custom DNS etc.):
//   - dns: custom replaces auto-generated
//   - outbounds: custom entries appended
//   - routing.rules: custom rules prepended (matched first)
//   - policy, api: custom deep-merges
//   - inbounds: NOT merged (panel-managed, authoritative)
func mergeCustomXray(cfg M, kcfg config.KernelConfig) {
	custom, err := kernel.LoadCustomConfig(kcfg.CustomConfig)
	if err != nil {
		nlog.Core().Error("failed to load custom xray config", "error", err)
		return
	}
	if custom == nil {
		return
	}

	// dns — custom replaces (same as V2bX DnsConfigPath)
	if v, ok := custom["dns"]; ok {
		cfg["dns"] = v
	}

	// outbounds — append custom entries
	if v, ok := custom["outbounds"]; ok {
		if existing, ok := cfg["outbounds"].([]M); ok {
			cfg["outbounds"] = kernel.MergeAppendList(existing, v)
		}
	}

	// routing — merge sub-fields
	if customRouting, ok := custom["routing"]; ok {
		if customRoutingMap, ok := customRouting.(map[string]any); ok {
			mergeCustomXrayRouting(cfg, customRoutingMap)
		}
	}

	// other top-level keys (policy, api, transport, etc.) — custom overrides,
	// but we protect auto-generated inbounds, stats, log, routing, outbounds
	protected := map[string]bool{
		"inbounds": true, "outbounds": true, "routing": true,
		"dns": true, "log": true, "stats": true, "policy": true,
	}
	for k, v := range custom {
		if !protected[k] {
			cfg[k] = v
		}
	}
}

func mergeCustomXrayRouting(cfg M, customRouting map[string]any) {
	routing, ok := cfg["routing"].(M)
	if !ok {
		routing = M{}
		cfg["routing"] = routing
	}

	// rules — custom rules prepended (so they match before panel rules)
	if v, ok := customRouting["rules"]; ok {
		if existing, ok := routing["rules"].([]M); ok {
			routing["rules"] = kernel.MergePrependList(existing, v)
		}
	}

	// domainStrategy, balancers, etc. — custom overrides
	for k, v := range customRouting {
		if k == "rules" {
			continue
		}
		routing[k] = v
	}
}

func xrayLogLevel(singboxLevel string) string {
	switch singboxLevel {
	case "trace", "debug":
		return "debug"
	case "info":
		return "info"
	case "warn":
		return "warning"
	case "error":
		return "error"
	case "fatal", "panic":
		return "error"
	default:
		return "warning"
	}
}

func buildInbound(kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	// A landing node only serves the internal transit inbound; panel users never reach it.
	if nc.IsRelayLanding() {
		return buildRelayLandingInbound(kcfg, nc, tc)
	}

	listenAddr := "::"
	if nc.ListenIP != "" {
		listenAddr = nc.ListenIP
	}
	base := M{
		"tag":      nc.Protocol + "-in",
		"listen":   listenAddr,
		"port":     nc.ServerPort,
		"protocol": nc.Protocol,
		"streamSettings": M{
			"sockopt": M{
				"reusePort": true,
			},
		},
	}

	switch nc.Protocol {
	case "vmess":
		return buildVMess(base, kcfg, nc, users, tc)
	case "vless":
		return buildVLESS(base, kcfg, nc, users, tc)
	case "trojan":
		return buildTrojan(base, kcfg, nc, users, tc)
	case "shadowsocks":
		return buildShadowsocks(base, nc, users)
	case "socks":
		return buildSocks(base, users)
	case "http":
		return buildHTTP(base, kcfg, nc, users, tc)
	case "hysteria":
		return buildHysteria(base, nc, users, tc)
	default:
		return nil
	}
}

// userEmail returns the stats-tracking email for a user.
// Format: "user@<id>" so we can parse back the user ID from stats counters.
func userEmail(userID int) string {
	return fmt.Sprintf("user@%d", userID)
}

func buildVMess(base M, kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	clients := make([]M, 0, len(users))
	for _, u := range users {
		clients = append(clients, M{
			"id":      u.UUID,
			"alterId": 0,
			"email":   userEmail(u.ID),
		})
	}
	base["settings"] = M{"clients": clients}

	applyStreamSettings(base, kcfg, nc, tc)
	return base
}

func buildVLESS(base M, kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	clients := make([]M, 0, len(users))
	for _, u := range users {
		client := M{
			"id":    u.UUID,
			"email": userEmail(u.ID),
		}
		if nc.Flow != "" {
			client["flow"] = nc.Flow
		}
		clients = append(clients, client)
	}
	decryption := "none"
	if nc.Decryption != "" {
		decryption = nc.Decryption
	}
	base["settings"] = M{
		"clients":    clients,
		"decryption": decryption,
	}

	applyStreamSettings(base, kcfg, nc, tc)
	return base
}

func buildTrojan(base M, kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	clients := make([]M, len(users))
	for i := range users {
		u := &users[i]
		clients[i] = M{
			"password": u.UUID,
			"email":    userEmail(u.ID),
		}
	}
	base["settings"] = M{"clients": clients}

	applyStreamSettings(base, kcfg, nc, tc)

	// Trojan requires TLS or Reality to be enabled.
	// If the panel didn't explicitly set TLS=1 or TLS=2, but we have certs,
	// we should enable a default TLS config to ensure the inbound can start.
	ss, _ := base["streamSettings"].(M)
	if security, ok := ss["security"].(string); !ok || (security != "tls" && security != "reality") {
		nc.TLS = 1 // Force internal state to trigger TLS build in applyStreamSettings
		applyStreamSettings(base, kcfg, nc, tc)
	}

	return base
}

type ss2022Config struct {
	method string
	size   int
}

var ss2022Methods = map[string]ss2022Config{
	"2022-blake3-aes-128-gcm":       {"2022-blake3-aes-128-gcm", 16},
	"2022-blake3-aes-256-gcm":       {"2022-blake3-aes-256-gcm", 32},
	"2022-blake3-chacha20-poly1305": {"2022-blake3-chacha20-poly1305", 32},
}

// ss2022UserKey 与面板生成订阅密码时的 uuidToBase64 规则保持一致：
// 截取 UUID 文本的前 size 个字节，再使用标准 Base64 编码。
// 完整配置和 UserManager 热更新必须复用同一转换，否则 UUID 轮换后
// 动态加入的用户会得到与客户端订阅不同的 SS2022 密钥。
func ss2022UserKey(uuid string, size int) string {
	raw := []byte(uuid)
	if len(raw) > size {
		raw = raw[:size]
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func buildShadowsocks(base M, nc *model.NodeSpec, users []model.UserSpec) M {
	ss2022, isSS2022 := ss2022Methods[nc.Cipher]

	clients := make([]M, 0, len(users))

	if isSS2022 {
		// SS2022: server key at top level, per-user key must be Base64 of fixed-length raw bytes.
		// Only blake3-aes-* supports multi-user in Xray; chacha20 variant is single-user only.
		for i := range users {
			u := &users[i]
			clients = append(clients, M{
				"password": ss2022UserKey(u.UUID, ss2022.size),
				"email":    userEmail(u.ID),
			})
		}
		base["settings"] = M{
			"method":   nc.Cipher,
			"password": nc.ServerKey,
			"clients":  clients,
			"network":  "tcp,udp",
		}
	} else {
		// Traditional ciphers: multi-user via clients array.
		// Each entry must carry its own "method" field for Xray to parse correctly.
		for i := range users {
			u := &users[i]
			clients = append(clients, M{
				"method":   nc.Cipher,
				"password": u.UUID,
				"email":    userEmail(u.ID),
			})
		}
		base["settings"] = M{
			"method":  nc.Cipher,
			"clients": clients,
			"network": "tcp,udp",
		}
	}
	return base
}

func buildSocks(base M, users []model.UserSpec) M {
	base["protocol"] = "socks"
	accounts := make([]M, 0, len(users))
	for _, u := range users {
		accounts = append(accounts, M{
			"user":  u.UUID,
			"pass":  u.UUID,
			"email": userEmail(u.ID),
		})
	}
	base["settings"] = M{
		"auth":     "password",
		"accounts": accounts,
		"udp":      true,
	}
	return base
}

func buildHTTP(base M, kcfg config.KernelConfig, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	base["protocol"] = "http"
	accounts := make([]M, 0, len(users))
	for _, u := range users {
		accounts = append(accounts, M{
			"user":  u.UUID,
			"pass":  u.UUID,
			"email": userEmail(u.ID),
		})
	}
	base["settings"] = M{
		"accounts": accounts,
	}

	if nc.TLS == 1 {
		applyStreamSettings(base, kcfg, nc, tc)
	}
	return base
}

// buildHysteria creates a Hysteria v2 inbound for xray-core.
func buildHysteria(base M, nc *model.NodeSpec, users []model.UserSpec, tc kernel.TLSCert) M {
	if nc.Version != 2 {
		nlog.Core().Warn("xray: only supports hysteria v2, skipping v1 inbound")
		return nil
	}

	clients := make([]M, 0, len(users))
	for _, u := range users {
		clients = append(clients, M{
			"auth":  u.UUID,
			"email": userEmail(u.ID),
		})
	}
	base["settings"] = M{
		"version": nc.Version,
		"clients": clients,
	}

	ss := M{
		"network": "hysteria",
		"hysteriaSettings": M{
			"version":        nc.Version,
			"udpIdleTimeout": 60,
		},
	}

	finalMask := M{}
	if nc.UpMbps > 0 || nc.DownMbps > 0 {
		quicParams := M{}
		if nc.UpMbps > 0 {
			quicParams["brutalUp"] = fmt.Sprintf("%d mbps", nc.UpMbps)
		}
		if nc.DownMbps > 0 {
			quicParams["brutalDown"] = fmt.Sprintf("%d mbps", nc.DownMbps)
		}
		finalMask["quicParams"] = quicParams
	}
	if nc.Obfs == "salamander" {
		// 与客户端订阅使用同一份混淆参数，不能只把它下发给客户端。
		finalMask["udp"] = []M{{
			"type":     "salamander",
			"settings": M{"password": nc.ObfsPassword},
		}}
	}
	if len(finalMask) > 0 {
		ss["finalMask"] = finalMask
	}

	if tc.HasCert() {
		ss["security"] = "tls"
		tlsCert := M{
			"certificate": []string{string(tc.CertPEM)},
			"key":         []string{string(tc.KeyPEM)},
		}
		tlsSettings := M{
			"certificates": []M{tlsCert},
			"alpn":         []string{"h3"},
		}
		if echKeys := extractECHServerKeys(nc.TLSSettings); echKeys != "" {
			tlsSettings["echServerKeys"] = echKeys
		}
		ss["tlsSettings"] = tlsSettings
	} else {
		nlog.Core().Warn("hysteria requires TLS certificate files; configure cert_mode (self, file, http, dns, or content)")
	}

	if nc.GetProxyProtocol() {
		ss["sockopt"] = M{"acceptProxyProtocol": true}
	}

	base["streamSettings"] = ss
	return base
}

func applyStreamSettings(base M, kcfg config.KernelConfig, nc *model.NodeSpec, tc kernel.TLSCert) {
	transportAuth := ""
	if nc.IsRelayLanding() && nc.Relay.VLESS != nil {
		transportAuth = nc.Relay.VLESS.TransportAuth
	}
	ss := buildTransportStreamSettings(nc.Network, nc.NetworkSettings, transportAuth)
	network, _ := model.NormalizeRelayVLESSNetwork(nc.Network)

	// TLS
	if nc.TLS == 1 {
		tlsSettings := M{}
		serverName := nc.ServerName
		if serverName == "" && nc.Host != "" {
			serverName = nc.Host
		}
		if serverName != "" {
			tlsSettings["serverName"] = serverName
		}
		if nc.TLSSettings != nil {
			if sn, ok := nc.TLSSettings["server_name"]; ok && sn != "" {
				tlsSettings["serverName"] = sn
			}
			// ECH server-side: extract key from tls_settings.ech
			if echKeys := extractECHServerKeys(nc.TLSSettings); echKeys != "" {
				tlsSettings["echServerKeys"] = echKeys
			}
		}
		if network == "hysteria" {
			tlsSettings["alpn"] = []string{"h3"}
		}
		if tc.HasCert() {
			tlsCert := M{
				"certificate": []string{string(tc.CertPEM)},
				"key":         []string{string(tc.KeyPEM)},
			}
			tlsSettings["certificates"] = []M{tlsCert}
		} else {
			// Fallback placeholder for auto-TLS environments.
			// Xray allows empty certificates array in more cases than sing-box,
			// but providing a placeholder helps documentation.
		}
		ss["security"] = "tls"
		ss["tlsSettings"] = tlsSettings
	} else if nc.TLS == 2 {
		ss["security"] = "reality"
		ss["realitySettings"] = buildRealitySettings(kcfg, nc)
	}

	// Proxy Protocol
	if nc.GetProxyProtocol() {
		sockopt, ok := base["streamSettings"].(M)["sockopt"].(M)
		if !ok {
			sockopt = M{}
		}
		sockopt["acceptProxyProtocol"] = true
		ss["sockopt"] = sockopt
	}

	base["streamSettings"] = ss
}

func buildRealitySettings(kcfg config.KernelConfig, nc *model.NodeSpec) M {
	// minClientVer is always emitted: xray-core falls back to its own built-in
	// floor (26.3.27) when the field is absent, which would reject older clients.
	reality := M{
		"show":         false,
		"minClientVer": kcfg.RealityMinClientVersion(),
	}

	if nc.TLSSettings == nil {
		return reality
	}

	if pk, ok := nc.TLSSettings["private_key"]; ok {
		reality["privateKey"] = pk
	}
	if sid, ok := nc.TLSSettings["short_id"]; ok {
		switch v := sid.(type) {
		case string:
			reality["shortIds"] = []string{v}
		case []interface{}:
			ids := make([]string, 0, len(v))
			for _, item := range v {
				ids = append(ids, fmt.Sprintf("%v", item))
			}
			reality["shortIds"] = ids
		}
	}

	if dest, ok := nc.TLSSettings["dest"]; ok {
		destStr := fmt.Sprintf("%v", dest)
		reality["dest"] = destStr
	}
	if sn, ok := nc.TLSSettings["server_name"]; ok {
		reality["serverNames"] = []string{fmt.Sprintf("%v", sn)}
		if _, exists := reality["dest"]; !exists {
			reality["dest"] = fmt.Sprintf("%v:443", sn)
		}
	}

	return reality
}

// buildRouting assembles the rule list in priority order:
//  0. REALITY 防盗用规则 — 安全边界，不允许被面板路由覆盖或绕开
//  1. structured custom route rules (explicit panel overrides)
//  2. raw custom routes (native escape hatch)
//  3. built-in private/loopback blocklist
//  4. relay rules — a logical node's exit must not be overridden by generic panel routes
//  5. panel routes
func buildRouting(rules []model.RouteRule, customRouteRules []model.CustomRouteRule, customRules []map[string]any, relayRules []M, guardRules []M) M {
	var xrayRules []M

	// 防盗用规则必须排在所有面板规则之前，否则管理员的通用规则会放行或阻断伪装回源。
	xrayRules = append(xrayRules, guardRules...)

	// Structured custom routes now take the highest priority for panel-managed overrides.
	for _, rule := range customRouteRules {
		if rule.Disabled {
			continue
		}
		xrayRules = append(xrayRules, compileCustomRouteRule(rule)...)
	}

	// Raw custom routes remain the escape hatch, but no longer outrank structured rules.
	for _, cr := range customRules {
		xrayRules = append(xrayRules, M(cr))
	}

	xrayRules = append(xrayRules, M{
		"type": "field",
		"ip": []string{
			"10.0.0.0/8",
			"100.64.0.0/10",
			"127.0.0.0/8",
			"169.254.0.0/16",
			"172.16.0.0/12",
			"192.0.0.0/24",
			"192.168.0.0/16",
			"198.18.0.0/15",
			"fc00::/7",
			"fe80::/10",
			"::1/128",
		},
		"outboundTag": "block",
	})

	xrayRules = append(xrayRules, relayRules...)

	for _, rule := range rules {
		xrayRules = append(xrayRules, compilePanelRouteRule(rule)...)
	}

	return M{
		"domainStrategy": "AsIs",
		"rules":          xrayRules,
	}
}

func compilePanelRouteRule(rule model.RouteRule) []M {
	if len(rule.Match) == 0 {
		return nil
	}
	match := model.RouteMatch{}
	for _, item := range rule.Match {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "geoip:") || strings.Contains(item, "/") {
			match.IPCIDRs = append(match.IPCIDRs, item)
			continue
		}
		if strings.HasPrefix(item, "geosite:") {
			match.Domains = append(match.Domains, item)
			continue
		}
		match.DomainSuffixes = append(match.DomainSuffixes, strings.TrimPrefix(item, "*."))
	}
	action := model.RouteAction{Type: "block"}
	switch rule.Action {
	case "direct":
		action.Type = "direct"
	case "proxy":
		action.Type = "route"
		action.Target = rule.ActionValue
	}
	return compileCustomRouteRule(model.CustomRouteRule{Match: match, Action: action})
}

func compileCustomRouteRule(rule model.CustomRouteRule) []M {
	outbound := xrayOutboundForAction(rule.Action)
	var compiled []M

	if len(rule.Match.Domains) > 0 || len(rule.Match.DomainSuffixes) > 0 {
		domains := make([]string, 0, len(rule.Match.Domains)+len(rule.Match.DomainSuffixes))
		for _, value := range rule.Match.Domains {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			domains = append(domains, value)
		}
		for _, value := range rule.Match.DomainSuffixes {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			domains = append(domains, "domain:"+value)
		}
		if len(domains) > 0 {
			compiled = append(compiled, M{
				"type":        "field",
				"domain":      domains,
				"outboundTag": outbound,
			})
		}
	}
	if len(rule.Match.IPCIDRs) > 0 {
		compiled = append(compiled, M{
			"type":        "field",
			"ip":          copyStrings(rule.Match.IPCIDRs),
			"outboundTag": outbound,
		})
	}
	if len(rule.Match.Ports) > 0 {
		compiled = append(compiled, M{
			"type":        "field",
			"port":        strings.Join(rule.Match.Ports, ","),
			"outboundTag": outbound,
		})
	}
	if len(rule.Match.Networks) > 0 {
		compiled = append(compiled, M{
			"type":        "field",
			"network":     strings.Join(rule.Match.Networks, ","),
			"outboundTag": outbound,
		})
	}
	if len(rule.Match.SourceCIDRs) > 0 {
		compiled = append(compiled, M{
			"type":        "field",
			"source":      copyStrings(rule.Match.SourceCIDRs),
			"outboundTag": outbound,
		})
	}
	if len(rule.Match.SourcePorts) > 0 {
		compiled = append(compiled, M{
			"type":        "field",
			"sourcePort":  strings.Join(rule.Match.SourcePorts, ","),
			"outboundTag": outbound,
		})
	}
	return compiled
}

func xrayOutboundForAction(action model.RouteAction) string {
	switch action.Type {
	case "direct":
		return "direct"
	case "route":
		if action.Target != "" {
			return action.Target
		}
		return "block"
	default:
		return "block"
	}
}

func copyStrings(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// sanitizeEmptyArrays recursively removes empty slices from a map.
// PHP encodes empty objects as [] instead of {}; xray-core rejects []
// for fields that expect struct types (e.g. sockopt, tlsSettings).
func sanitizeEmptyArrays(m map[string]interface{}) {
	for k, v := range m {
		switch val := v.(type) {
		case []interface{}:
			if len(val) == 0 {
				delete(m, k)
			}
		case map[string]interface{}:
			sanitizeEmptyArrays(val)
		}
	}
}

// validateHysteriaECHKeys 校验实际生成的密钥，避免再次读取文件时与配置内容不一致。
func validateHysteriaECHKeys(nc *model.NodeSpec, cfg M) error {
	if nc.Protocol != "hysteria" || nc.Version != 2 {
		return nil
	}
	ech, _ := nc.TLSSettings["ech"].(map[string]any)
	if ech["enabled"] != true {
		return nil
	}
	var encoded string
	inbounds, _ := cfg["inbounds"].([]M)
	for _, inbound := range inbounds {
		if inbound["protocol"] != "hysteria" {
			continue
		}
		stream, _ := inbound["streamSettings"].(M)
		tlsSettings, _ := stream["tlsSettings"].(M)
		encoded, _ = tlsSettings["echServerKeys"].(string)
		break
	}
	if encoded == "" {
		return fmt.Errorf("hysteria2 ECH: missing or unreadable server keys")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("hysteria2 ECH: invalid server key encoding")
	}
	keys, err := xrayTLS.ConvertToGoECHKeys(data)
	if err != nil || len(keys) == 0 {
		return fmt.Errorf("hysteria2 ECH: invalid server key list")
	}
	for _, key := range keys {
		if len(key.PrivateKey) == 0 || len(key.Config) == 0 {
			return fmt.Errorf("hysteria2 ECH: invalid server key entry")
		}
	}
	return nil
}

// extractECHServerKeys extracts the ECH private key from tls_settings.ech
// and converts it from PEM to base64 for xray-core's echServerKeys field.
func extractECHServerKeys(tlsSettings map[string]interface{}) string {
	echRaw, ok := tlsSettings["ech"]
	if !ok {
		return ""
	}
	echMap, ok := echRaw.(map[string]interface{})
	if !ok {
		return ""
	}
	enabled, _ := echMap["enabled"].(bool)
	if !enabled {
		return ""
	}

	// Try inline key first, then key_path
	var pemData []byte
	if key, _ := echMap["key"].(string); key != "" {
		pemData = []byte(key)
	} else if keyPath, _ := echMap["key_path"].(string); keyPath != "" {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			nlog.Core().Warn("ECH enabled but key_path read failed", "error", err)
			return ""
		}
		pemData = data
	} else {
		nlog.Core().Warn("ECH enabled but no key or key_path provided")
		return ""
	}

	return echPEMToBase64(pemData)
}

// echPEMToBase64 parses an "ECH KEYS" PEM block and returns base64 of the raw bytes.
// If the input is not valid PEM or has the wrong type, it returns empty string.
func echPEMToBase64(data []byte) string {
	trimmed := strings.TrimSpace(string(data))
	if !strings.Contains(trimmed, "-----") {
		// Assume already raw base64
		return trimmed
	}
	block, _ := pem.Decode(data)
	if block == nil {
		nlog.Core().Warn("ECH key: invalid PEM data")
		return ""
	}
	if block.Type != "ECH KEYS" {
		nlog.Core().Warn("ECH key: unexpected PEM type", "expected", "ECH KEYS", "got", block.Type)
		return ""
	}
	return base64.StdEncoding.EncodeToString(block.Bytes)
}
