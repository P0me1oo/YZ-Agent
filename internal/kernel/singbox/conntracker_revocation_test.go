package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestUnknownUserRejectedExceptInternalWireGuardAndLanding(t *testing.T) {
	for _, kind := range []string{"proxy", "entry", "wireguard", "landing"} {
		t.Run(kind, func(t *testing.T) {
			node := &model.NodeSpec{Protocol: "vless"}
			allowed := false
			switch kind {
			case "entry":
				node = testRelayNode()
			case "wireguard":
				node.Protocol, allowed = "wireguard", true
			case "landing":
				node.Relay = &model.RelayConfig{Mode: "landing"}
				allowed = true
			}
			tracker := NewConnTracker(0)
			tracker.setNodeUsers(node, nil)
			for _, name := range []string{"", "removed-user"} {
				t.Run(name, func(t *testing.T) {
					conn := &testConn{}
					wrapped := tracker.RoutedConnection(
						t.Context(),
						conn,
						testInboundContext(name, "198.51.100.7"),
						nil,
						nil,
					)
					if conn.closed == allowed {
						t.Fatalf("未知用户的准入结果错误: kind=%s allowed=%v closed=%v", kind, allowed, conn.closed)
					}
					if allowed {
						_ = wrapped.Close()
					}
					traffic, _, _ := tracker.GetUserTraffic()
					if len(traffic) != 0 {
						t.Fatalf("内部或已拒绝连接被记为用户流量: %v", traffic)
					}
				})
			}
		})
	}
}
