package singbox

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xkernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"testing"
	"time"
)

func TestOperationsProbeBothCoresExcludesUserAccounting(t *testing.T) {
	for _, name := range []string{"xray", "singbox"} {
		t.Run(name, func(t *testing.T) {
			var k kernel.Kernel
			if name == "xray" {
				k = xkernel.New(config.KernelConfig{})
			} else {
				k = New(config.KernelConfig{})
			}
			node := &model.NodeSpec{Protocol: "vmess", ListenIP: "127.0.0.1", ServerPort: runtimeDualPort(t)}
			if name == "xray" {
				node.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"127.0.0.1"}, "outboundTag": "direct"}}
			}
			users := []model.UserSpec{{ID: 1, UUID: "fbdbb984-3b9e-4acd-a736-8cc1b90a9d4d"}}
			if err := k.Start(node, users, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			defer k.Stop()
			target := runtimeEcho(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := k.(interface {
				ProbeDial(context.Context, string, string, string) (net.Conn, error)
			}).ProbeDial(ctx, "direct", "tcp", target.String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte("probe")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 5)
			if _, err := io.ReadFull(conn, buf); err != nil {
				t.Fatal(err)
			}
			traffic, _, _, err := k.GetUserTraffic(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range traffic {
				if v[0] != 0 || v[1] != 0 {
					t.Fatal("probe billed", traffic)
				}
			}
		})
	}
}
func TestXrayRouteHotUpdatePreservesExistingConnection(t *testing.T) {
	node := runtimeNode(t, "vmess")
	users := []model.UserSpec{runtimeUser(t, 987)}
	target := runtimeEcho(t)
	k := relayStartCore(t, "xray", node, users, kernel.TLSCert{}, target.String())
	destination := singM.ParseSocksaddr("198.51.100.10:80")
	client := runtimeClient(t, node, users[0])
	conn := runtimeDial(t, client, destination)
	runtimeExchange(t, conn)
	next := *node
	next.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"198.51.100.10"}, "outboundTag": "block"}}
	if err := k.Reload(&next, users, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	runtimeExchange(t, conn)
	runtimeReject(t, client, destination)
}
