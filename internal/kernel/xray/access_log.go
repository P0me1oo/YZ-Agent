package xray

import (
	"context"
	"reflect"
	"strings"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	proxymanOutbound "github.com/xtls/xray-core/app/proxyman/outbound"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/transport"
)

type accessLogContextKey struct{}
type blockLogContextKey struct{}
type accessLogNodesContextKey struct{}
type accessLogRequestKey struct{}

type accessLogRequest struct {
	user    int
	dest    net.Destination
	once    sync.Once
	landing bool
}

// 在最终出站收到请求时采集，避免把自定义路由改选的落地记成认证身份中的原线路。
// 嵌入原处理器，保留拨号、WG 和出站查询等接口。
type accessLogOutbound struct {
	*proxymanOutbound.Handler
	recorder *accesslog.Recorder
	blockLog *accesslog.Recorder
	nodes    map[string]int
}

func init() {
	configType := reflect.TypeOf((*xrayCore.OutboundHandlerConfig)(nil))
	original := typeCreatorRegistry[configType]
	typeCreatorRegistry[configType] = func(ctx context.Context, config interface{}) (interface{}, error) {
		raw, err := original(ctx, config)
		recorder, _ := ctx.Value(accessLogContextKey{}).(*accesslog.Recorder)
		blockLog, _ := ctx.Value(blockLogContextKey{}).(*accesslog.Recorder)
		handler, ok := raw.(*proxymanOutbound.Handler)
		if err != nil || (recorder == nil && blockLog == nil) || !ok {
			return raw, err
		}
		nodes, _ := ctx.Value(accessLogNodesContextKey{}).(map[string]int)
		return &accessLogOutbound{Handler: handler, recorder: recorder, blockLog: blockLog, nodes: nodes}, nil
	}
}

func (h *accessLogOutbound) Dispatch(ctx context.Context, link *transport.Link) {
	if request, _ := ctx.Value(accessLogRequestKey{}).(*accessLogRequest); request != nil {
		request.once.Do(func() {
			dest := request.dest
			if rule := model.PanelBlockID(h.Tag()); rule > 0 {
				domain, address := "", ""
				if dest.Address.Family().IsDomain() {
					domain = dest.Address.Domain()
				} else {
					address = dest.Address.IP().String()
				}
				h.blockLog.AddBlock(request.user, rule, domain, address, uint16(dest.Port), strings.ToLower(dest.Network.String()))
			}
			if request.landing {
				return
			}
			if dest.Address.Family().IsDomain() {
				h.recorder.AddForNode(h.nodes[h.Tag()], request.user, dest.Address.Domain(), "", uint16(dest.Port))
			} else {
				h.recorder.AddForNode(h.nodes[h.Tag()], request.user, "", dest.Address.IP().String(), uint16(dest.Port))
			}
		})
	}
	h.Handler.Dispatch(ctx, link)
}

// 仅在启动前注入；每个核心实例通过自己的上下文取得队列。
func (x *Xray) SetAccessLog(recorder *accesslog.Recorder) { x.accessLog = recorder }
func (x *Xray) SetBlockLog(recorder *accesslog.Recorder)  { x.blockLog = recorder }

func (d *LimitDispatcher) accessContext(ctx context.Context, user int, dest net.Destination) context.Context {
	inbound := session.InboundFromContext(ctx)
	if (d.accessLog == nil && d.blockLog == nil) || inbound == nil || dest.Address == nil {
		return ctx
	}
	if user <= 0 && inbound.User != nil {
		d.mu.RLock()
		user = d.emailToUID[inbound.User.Email]
		d.mu.RUnlock()
	}
	if user <= 0 {
		user = accesslog.ContextUser(ctx)
	}
	return context.WithValue(ctx, accessLogRequestKey{}, &accessLogRequest{user: user, dest: dest, landing: inbound.Tag == relayLandingInboundTag || accesslog.ContextUser(ctx) > 0})
}
