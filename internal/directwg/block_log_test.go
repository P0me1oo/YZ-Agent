package directwg_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	M "github.com/sagernet/sing/common/metadata"
)

func TestDirectWireGuardBlockLogUser(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		t.Run(kind, func(t *testing.T) {
			serverPriv, serverPub := keys(t)
			clientPriv, clientPub := keys(t)
			socket, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := socket.LocalAddr().(*net.UDPAddr).Port
			socket.Close()
			cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
			var base kernel.Kernel
			if kind == "xray" {
				base = xray.New(cfg)
			} else {
				base = singbox.New(cfg)
			}
			core := directwg.Wrap(base)
			r := &accesslog.Recorder{NodeID: 5}
			r.Enable(true)
			core.(interface{ SetBlockLog(*accesslog.Recorder) }).SetBlockLog(r)
			node := &model.NodeSpec{Protocol: "wireguard", KernelType: kind, ListenIP: "127.0.0.1", ServerPort: port,
				WireGuard: &panel.WireGuardConfig{PrivateKey: serverPriv, Address: []string{"10.0.0.1/32"}, MTU: 1420},
				Routes:    []model.RouteRule{{ID: 91, Action: "block", Match: []string{"198.51.100.10/32"}}}}
			users := []model.UserSpec{{ID: 21, UUID: "wg-block-test", WireGuard: &panel.WireGuardPeer{PublicKey: clientPub, Address: []string{"10.0.0.2/32"}}}}
			if err := core.Start(node, users, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(core.Stop)
			client := wgClient(t, clientPriv, serverPub, port, users[0].WireGuard.Address)
			for _, network := range []string{"tcp", "udp"} {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				conn, err := client.DialContext(ctx, network, M.ParseSocksaddr("198.51.100.10:443"))
				if err != nil {
					cancel()
					continue
				}
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				_, _ = conn.Write([]byte("block-test"))
				if _, err := conn.Read(make([]byte, 1)); err == nil {
					t.Fatal("被拦截的 WG 连接不应收到响应")
				}
				conn.Close()
				cancel()
			}
			batch, err := r.Next()
			if err != nil || batch == nil || len(batch.Records) != 2 {
				t.Fatalf("记录不完整: %+v %v", batch, err)
			}
			for _, record := range batch.Records {
				if record.UserID != 21 || record.NodeID != 5 || record.RuleID != 91 || record.Destination != "198.51.100.10" {
					t.Fatalf("普通 WG 身份未传递到实际拦截: %+v", record)
				}
			}
		})
	}
}
