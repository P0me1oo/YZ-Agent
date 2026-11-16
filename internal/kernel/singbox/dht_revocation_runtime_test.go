//go:build integration && with_quic

package singbox

import (
	"fmt"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestDHTExistingSessionKeepsUserAfterRevocation(t *testing.T) {
	for _, change := range []string{"remove", "rotate"} {
		t.Run(change, func(t *testing.T) {
			destination := singM.ParseSocksaddr(relayMarkerEcho(t, "direct"))
			node := runtimeNode(t, "vless")
			node.Routes = []model.RouteRule{{
				ID: 951, Action: "block", Protocols: []string{"bittorrent"},
				Match: []string{"127.0.0.1/32"}, Ports: []string{fmt.Sprint(destination.Port)}, Networks: []string{"udp"},
			}}
			user := runtimeUser(t, 2501)
			log := &accesslog.Recorder{NodeID: 31}
			log.Enable(true)
			core := dhtStartNative(t, "singbox", node, user, log)
			client := routeTestClient(t, node, user)
			conn := dhtSession(t, client, destination)
			dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
			next := []model.UserSpec{runtimeUser(t, 2502)}
			if change == "rotate" {
				rotated := user
				rotated.UUID = runtimeUser(t, 2503).UUID
				next = append(next, rotated)
			}
			if _, _, err := core.UpdateUsers(next); err != nil {
				t.Fatal(err)
			}
			// 已有会话仍转发和计费，后续拦截也应保留建立时的用户。
			dhtExchange(t, conn, destination, []byte(runtimePayload), "direct", false)
			dhtExchange(t, conn, destination, dhtPing(), "direct", true)
			records := accessRecords(t, log, 1)
			if len(records) != 1 || records[0].UserID != user.ID || records[0].RuleID != 951 {
				t.Fatalf("旧会话撤权后丢失 DHT 拦截用户归属: %+v", records)
			}
		})
	}
}
