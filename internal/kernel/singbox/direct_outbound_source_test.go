package singbox

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func TestLegacyDirectOutboundMappedDestination(t *testing.T) {
	node := runtimeNode(t, "shadowsocks2022-128")
	node.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: M{
		"send_through": "127.0.0.2",
	}}}
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	if err := s.Start(node, nil, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	outbound, exists := s.box.Outbound().Outbound("direct")
	if !exists {
		t.Fatal("直连出站未注册")
	}
	tcpTarget, tcpSources := directCompatTCPEcho(t, "127.0.0.1")
	udpTarget, udpSources := directCompatUDPEcho(t, "127.0.0.1")
	mapped := netip.MustParseAddr("::ffff:127.0.0.1")
	tcpTarget.Addr, udpTarget.Addr = mapped, mapped
	// 路由或 DNS 可以提供映射地址，必须按实际 IPv4 地址选择已经绑定的出口。
	for _, tc := range []struct {
		name string
		dial func() (net.Conn, error)
	}{
		{"literal", func() (net.Conn, error) {
			return outbound.DialContext(context.Background(), "tcp", tcpTarget)
		}},
		{"resolved", func() (net.Conn, error) {
			return outbound.(N.ParallelDialer).DialParallel(context.Background(), "tcp",
				singM.Socksaddr{Fqdn: "mapped.test", Port: tcpTarget.Port}, []netip.Addr{mapped})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := tc.dial()
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			runtimeExchange(t, conn)
			directCompatCheckSource(t, tcpSources, "127.0.0.2")
		})
	}
	packetConn := runtimeUDPDial(t, outbound, udpTarget)
	runtimeUDPExchange(t, packetConn, udpTarget)
	directCompatCheckSource(t, udpSources, "127.0.0.2")
}
