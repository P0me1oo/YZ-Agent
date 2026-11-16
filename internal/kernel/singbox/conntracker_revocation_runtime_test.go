//go:build with_quic

package singbox

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestSingBoxRevokedQUICSessionRejectsNewRequests(t *testing.T) {
	for _, protocol := range []string{"hysteria2", "tuic"} {
		for _, change := range []string{"remove", "rotate"} {
			t.Run(protocol+"/"+change, func(t *testing.T) {
				node := runtimeNode(t, protocol)
				first, kept := runtimeUser(t, 9101), runtimeUser(t, 9102)
				core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
				if err := core.Start(node, []model.UserSpec{first, kept}, runtimeCertificate(t)); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				target := runtimeEcho(t)
				client := runtimeClient(t, node, first)
				original := runtimeDial(t, client, target)
				runtimeExchange(t, original)
				unit := int64(len(runtimePayload))
				runtimeWaitTraffic(t, core, first.ID, [2]int64{unit, unit})
				next := []model.UserSpec{kept}
				if change == "rotate" {
					first.UUID = runtimeUser(t, 9103).UUID
					next = []model.UserSpec{first, kept}
				}
				if _, _, err := core.UpdateUsers(next); err != nil {
					t.Fatal(err)
				}
				// 已有连接保留计费；已撤销身份不能借旧 QUIC 会话再创建代理请求。
				runtimeExchange(t, original)
				runtimeWaitTraffic(t, core, first.ID, [2]int64{2 * unit, 2 * unit})
				t.Run("tcp", func(t *testing.T) { runtimeReject(t, client, target) })
				t.Run("udp", func(t *testing.T) { rejectRevokedUDP(t, client, runtimeUDPEcho(t)) })
				runtimeWaitTraffic(t, core, first.ID, [2]int64{2 * unit, 2 * unit})
				runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, kept), target))
				runtimeWaitTraffic(t, core, kept.ID, [2]int64{unit, unit})
			})
		}
	}
}

func rejectRevokedUDP(t *testing.T, client adapter.Outbound, target singM.Socksaddr) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := client.ListenPacket(ctx, target)
	if err != nil {
		if strings.Contains(err.Error(), "network changed") {
			t.Fatalf("网络监控未稳定，不能判断撤权效果: %v", err)
		}
		return
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("测试客户端无法设置读取超时: %v", err)
	}
	if _, err := conn.WriteTo([]byte(runtimePayload), target.UDPAddr()); err != nil {
		return
	}
	response := make([]byte, 2048)
	if _, _, err := conn.ReadFrom(response); err == nil {
		t.Fatal("已撤销身份通过旧 QUIC 会话建立 UDP 请求")
	}
}
