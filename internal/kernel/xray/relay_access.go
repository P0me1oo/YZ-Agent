package xray

import (
	"context"
	"strconv"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/common/session"
)

type relayTracking struct {
	key            string
	cancel         context.CancelFunc
	closeTransport func()
}

func relayTrackingFor(ctx context.Context, cancel context.CancelFunc) relayTracking {
	tracking := relayTracking{key: relayAccessKey(ctx), cancel: cancel}
	if inbound := session.InboundFromContext(ctx); inbound != nil && inbound.Conn != nil {
		conn := inbound.Conn
		tracking.closeTransport = func() { _ = conn.Close() }
	}
	return tracking
}

func relayAccessKey(ctx context.Context) string {
	inbound := session.InboundFromContext(ctx)
	if inbound == nil || inbound.User == nil {
		return ""
	}
	return inbound.User.Email + ":" + strconv.Itoa(int(inbound.VlessRoute))
}

// 使用认证会话保留的线路编号校验，拒绝旧配置及旧 QUIC 会话发起的新请求。
func (d *LimitDispatcher) SetRelayAccess(node *model.NodeSpec, users []model.UserSpec) {
	var allowed map[string]bool
	if node.IsRelayEntry() {
		allowed = make(map[string]bool)
		for _, user := range users {
			for _, route := range node.RelayRouteIDs() {
				if user.AllowsRelayRoute(route) {
					allowed[userEmail(user.ID)+":"+strconv.Itoa(route)] = true
				}
			}
		}
	} else if d.relayRoutes.Load() != nil {
		// 已移除中转拓扑的旧实例仍可能在排空，不能继续承载原线路。
		allowed = map[string]bool{}
	}
	d.relayAccess.Replace(allowed)
}
