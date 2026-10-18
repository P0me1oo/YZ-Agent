package singbox

import (
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/sagernet/sing-box/adapter"
)

// 在启动前注入，重载创建的新追踪器继续使用同一队列。
func (s *SingBox) SetAccessLog(recorder *accesslog.Recorder) { s.accessLog = recorder }

func (t *ConnTracker) recordAccess(user int, metadata adapter.InboundContext, outbound adapter.Outbound) {
	if metadata.Inbound == relayLandingInboundTag {
		return
	}
	domain := metadata.Domain
	if metadata.Destination.IsFqdn() {
		domain = metadata.Destination.Fqdn
	}
	node := 0
	if outbound != nil {
		t.usersMu.RLock()
		node = t.relayNodes[outbound.Tag()]
		t.usersMu.RUnlock()
	}
	t.accessLog.AddForNode(node, user, domain, metadata.Destination.Addr.String(), metadata.Destination.Port)
}
