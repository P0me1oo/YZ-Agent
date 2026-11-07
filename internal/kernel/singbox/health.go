package singbox

import (
	"context"
	"fmt"
	singM "github.com/sagernet/sing/common/metadata"
	"net"
)

func (s *SingBox) ProbeDial(ctx context.Context, tag, network, address string) (net.Conn, error) {
	s.mu.RLock()
	instance := s.box
	s.mu.RUnlock()
	if instance == nil {
		return nil, fmt.Errorf("核心未运行")
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
