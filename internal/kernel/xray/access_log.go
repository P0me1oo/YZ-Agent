package xray

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
)

type accessLogContextKey struct{}

// 仅在启动前注入；每个核心实例通过自己的上下文取得队列。
func (x *Xray) SetAccessLog(recorder *accesslog.Recorder) { x.accessLog = recorder }

func (d *LimitDispatcher) recordAccess(ctx context.Context, user int, dest net.Destination) {
	inbound := session.InboundFromContext(ctx)
	if inbound == nil || inbound.Tag == relayLandingInboundTag || dest.Address == nil {
		return
	}
	if user <= 0 && inbound.User != nil {
		d.mu.RLock()
		user = d.emailToUID[inbound.User.Email]
		d.mu.RUnlock()
	}
	if dest.Address.Family().IsDomain() {
		d.accessLog.Add(user, dest.Address.Domain(), "", uint16(dest.Port))
	} else {
		d.accessLog.Add(user, "", dest.Address.IP().String(), uint16(dest.Port))
	}
}
