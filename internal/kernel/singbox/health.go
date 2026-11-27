package singbox

import (
	"context"
	"fmt"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
	"net"
)

type healthProbeContextKey struct{}

func (s *SingBox) ProbeDial(ctx context.Context, tag, network, address string) (net.Conn, error) {
	s.mu.RLock()
	instance := s.box
	s.mu.RUnlock()
	if instance == nil {
		return nil, fmt.Errorf("核心未运行")
	}
	if tag == "" {
		if network != "tcp" {
			return nil, fmt.Errorf("诊断路由仅支持 TCP")
		}
		client, peer := net.Pipe()
		probeContext := context.WithValue(ctx, healthProbeContextKey{}, true)
		stop := context.AfterFunc(ctx, func() { client.Close(); peer.Close() })
		instance.Router().RouteConnectionEx(probeContext, peer, adapter.InboundContext{Network: network, Destination: singM.ParseSocksaddr(address)}, func(error) { stop(); peer.Close() })
		return client, nil
	}
	out := instance.Outbound().Default()
	if tag != "" {
		var ok bool
		out, ok = instance.Outbound().Outbound(tag)
		if !ok {
			return nil, fmt.Errorf("出站不存在")
		}
	}
	if out == nil {
		return nil, fmt.Errorf("没有可用出站")
	}
	return out.DialContext(ctx, network, singM.ParseSocksaddr(address))
}
