package xray

import (
	"context"
	"fmt"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	xcore "github.com/xtls/xray-core/core"
	"net"
)

type healthProbeContextKey struct{}

func (x *Xray) ProbeDial(ctx context.Context, tag, network, address string) (net.Conn, error) {
	x.mu.Lock()
	inst := x.instance
	x.mu.Unlock()
	if inst == nil || !x.IsRunning() {
		return nil, fmt.Errorf("核心未运行")
	}
	dest, err := xnet.ParseDestination(network + ":" + address)
	if err != nil {
		return nil, err
	}
	if tag != "" {
		ctx = session.SetForcedOutboundTagToContext(ctx, tag)
	}
	ctx = context.WithValue(ctx, healthProbeContextKey{}, true)
	return xcore.Dial(ctx, inst, dest)
}
