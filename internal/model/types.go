package model

import (
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type NodeSpec struct {
	Protocol        string
	ListenIP        string
	ServerPort      int
	PortHopping     string
	Network         string
	NetworkSettings map[string]any
	Routes          []RouteRule

	KernelType       string
	KernelLogLevel   string
	CustomOutbounds  []OutboundConfig
	CustomRoutes     []map[string]any
	CustomRouteRules []CustomRouteRule
	CertConfig       *config.CertConfig
	AutoTLS          bool
	Domain           string

	Cipher    string
	Plugin    string
	PluginOpt string
	ServerKey string

	TLS         int
	Flow        string
	Decryption  string
	TLSSettings map[string]any

	Host       string
	ServerName string

	Version      int
	UpMbps       int
	DownMbps     int
	Obfs         string
	ObfsPassword string

	CongestionControl string
	PaddingScheme     string
	Transport         string
	TrafficPattern    string

	Multiplex           *MultiplexConfig
	AcceptProxyProtocol bool

	Relay *RelayConfig
}

const (
	relayModeEntry   = panel.RelayModeEntry
	relayModeLanding = panel.RelayModeLanding
)

// RelayConfig mirrors panel.RelayConfig in kernel-agnostic form.
type RelayConfig struct {
	Mode string

	// Entry-side.
	RouteID  int
	Children []RelayChild

	// Landing-side.
	Protocol    string
	ListenPort  int
	Cipher      string
	Password    string
	EntryNodeID int
	VLESS       *RelayVLESSConfig
}

type RelayChild struct {
	NodeID   int
	Tag      string
	RouteID  int
	Protocol string
	Address  string
	Port     int
	Cipher   string
	Password string
	VLESS    *RelayVLESSConfig
}

type RelayVLESSConfig struct {
	ID              string
	Network         string
	NetworkSettings map[string]any
	TLS             int
	Flow            string
	Encryption      string
	TLSSettings     map[string]any
	RealitySettings map[string]any
	TransportAuth   string
}

func (r *RelayConfig) IsEntry() bool {
	return r != nil && r.Mode == panel.RelayModeEntry
}

func (r *RelayConfig) IsLanding() bool {
	return r != nil && r.Mode == panel.RelayModeLanding
}

// IsRelayEntry reports whether this node hosts internal outbounds for logical nodes.
func (n *NodeSpec) IsRelayEntry() bool {
	return n != nil && n.Relay.IsEntry()
}

// IsRelayLanding reports whether this node only serves the internal transit inbound.
// Such a node has no panel users, so the kernel must stay up with an empty user set.
func (n *NodeSpec) IsRelayLanding() bool {
	return n != nil && n.Relay.IsLanding()
}

// UsesSS2022 判断当前节点的入站或受管中转出站是否依赖 SS2022 时间戳。
func (n *NodeSpec) UsesSS2022() bool {
	if n == nil {
		return false
	}
	protocol := strings.ToLower(strings.TrimSpace(n.Protocol))
	if (protocol == "shadowsocks" || protocol == "ss") && IsRelaySS2022Cipher(n.Cipher) {
		return true
	}
	if n.Relay == nil {
		return false
	}
	if n.Relay.IsLanding() {
		return strings.EqualFold(strings.TrimSpace(n.Relay.Protocol), "shadowsocks") &&
			IsRelaySS2022Cipher(n.Relay.Cipher)
	}
	if !n.Relay.IsEntry() {
		return false
	}
	for _, child := range n.Relay.Children {
		if strings.EqualFold(strings.TrimSpace(child.Protocol), "shadowsocks") &&
			IsRelaySS2022Cipher(child.Cipher) {
			return true
		}
	}
	return false
}

// RelayOutboundTags returns the internal outbound tags in a stable order.
func (n *NodeSpec) RelayOutboundTags() []string {
	if !n.IsRelayEntry() {
		return nil
	}
	tags := make([]string, 0, len(n.Relay.Children))
	for _, child := range n.Relay.Children {
		if child.Tag != "" {
			tags = append(tags, child.Tag)
		}
	}
	return tags
}

// RelayNodeIDByTag maps internal outbound tags back to logical node IDs so the
// entry can report per-landing traffic without re-deriving the tag format.
func (n *NodeSpec) RelayNodeIDByTag() map[string]int {
	if !n.IsRelayEntry() {
		return nil
	}
	out := make(map[string]int, len(n.Relay.Children))
	for _, child := range n.Relay.Children {
		if child.Tag != "" && child.NodeID > 0 {
			out[child.Tag] = child.NodeID
		}
	}
	return out
}

type OutboundConfig struct {
	Tag      string
	Protocol string
	Settings map[string]any
	ProxyTag string
}

type RouteRule struct {
	ID          int
	Match       []string
	Action      string
	ActionValue string
}

type MultiplexConfig struct {
	Enabled        bool
	Protocol       string
	MaxConnections int
	MinStreams     int
	MaxStreams     int
	Padding        bool
	Brutal         *BrutalConfig
}

type BrutalConfig struct {
	Enabled  bool
	UpMbps   int
	DownMbps int
}

type UserSpec struct {
	ID          int
	UUID        string
	SpeedLimit  int
	DeviceLimit int
}

func (n *NodeSpec) GetProxyProtocol() bool {
	if n == nil {
		return false
	}
	if n.AcceptProxyProtocol {
		return true
	}
	if n.NetworkSettings != nil {
		if v, ok := n.NetworkSettings["acceptProxyProtocol"]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
	}
	return false
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneMapSlice(src []map[string]any) []map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(src))
	for _, item := range src {
		out = append(out, cloneAnyMap(item))
	}
	return out
}

func cloneStringSlice(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}
