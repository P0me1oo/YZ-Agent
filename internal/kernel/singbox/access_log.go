package singbox

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
)

// 在启动前注入，重载创建的新追踪器继续使用同一队列。
func (s *SingBox) SetAccessLog(recorder *accesslog.Recorder) { s.accessLog = recorder }
func (s *SingBox) SetBlockLog(recorder *accesslog.Recorder)  { s.blockLog = recorder }

// 后续 UDP 报文的拦截来自核心观察接口；沿用已认证会话的身份，不重复登记连接。
func (t *ConnTracker) BlockedDHTPacket(
	ctx context.Context,
	metadata adapter.InboundContext,
	_ adapter.Rule,
	outbound adapter.Outbound,
) {
	user := accesslog.ContextUser(ctx)
	if user <= 0 {
		t.usersMu.RLock()
		user = t.uuidMap[metadata.User]
		t.usersMu.RUnlock()
	}
	t.recordBlock(ctx, user, metadata, outbound, "udp")
}

func (t *ConnTracker) recordBlock(ctx context.Context, user int, metadata adapter.InboundContext, outbound adapter.Outbound, network string) {
	if outbound == nil {
		return
	}
	rule := model.PanelBlockID(outbound.Tag())
	if rule == 0 {
		return
	}
	if user <= 0 {
		user = accesslog.ContextUser(ctx)
	}
	domain := metadata.Domain
	if metadata.Destination.IsFqdn() {
		domain = metadata.Destination.Fqdn
	}
	t.blockLog.AddBlock(user, rule, domain, metadata.Destination.Addr.String(), metadata.Destination.Port, network)
}

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
