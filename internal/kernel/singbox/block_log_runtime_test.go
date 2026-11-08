//go:build with_quic

package singbox

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestBlockLogRulesTCPUDPReloadAndDisabled(t *testing.T) {
	for _, kind := range []string{"xray", "singbox"} {
		for _, protocol := range []string{"vless", "shadowsocks"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				r := &accesslog.Recorder{NodeID: 17}
				r.Enable(true)
				node, user := runtimeNode(t, protocol), runtimeUser(t, 1701)
				node.Routes = []model.RouteRule{
					{ID: 701, Action: "block", Protocols: []string{"bittorrent"}},
					{ID: 702, Action: "block", Ports: []string{"6881"}, Networks: []string{"udp"}},
				}
				core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), nil, r)
				web, torrent := singM.ParseSocksaddr("198.51.100.10:80"), singM.ParseSocksaddr("198.51.100.10:6881")
				for run := 0; run < 2; run++ {
					if run > 0 {
						next := *node
						next.Routes = append([]model.RouteRule(nil), node.Routes...)
						next.Routes[0].ID = 703
						node = &next
						if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
							t.Fatal(err)
						}
					}
					client := routeTestClient(t, node, user)
					routeExpectPass(t, client, "tcp", torrent, []byte(runtimePayload))
					routeExpectPass(t, client, "udp", web, []byte(runtimePayload))
					if batch, _ := r.Next(); batch != nil {
						t.Fatal("正常请求被误记为拦截")
					}
					routeExpectBlocked(t, client, "tcp", web, routeTestBTHandshake())
					routeExpectBlocked(t, client, "udp", web, routeTestUTPSyn())
					routeExpectBlocked(t, client, "udp", torrent, []byte(runtimePayload))
					records := accessRecords(t, r, 3)
					if len(records) != 3 {
						t.Fatalf("重复记录: %+v", records)
					}
					counts := map[int]int{}
					for _, record := range records {
						if record.NodeID != 17 || record.UserID != user.ID || record.Destination != "198.51.100.10" || (record.Network != "tcp" && record.Network != "udp") {
							t.Fatalf("记录内容不正确: %+v", record)
						}
						counts[record.RuleID]++
					}
					if counts[node.Routes[0].ID] != 2 || counts[702] != 1 {
						t.Fatalf("未按命中规则记录: %v", counts)
					}
					r.Enable(false)
					routeExpectBlocked(t, client, "tcp", web, routeTestBTHandshake())
					if batch, _ := r.Next(); batch != nil {
						t.Fatal("关闭采集后仍记录")
					}
					r.Enable(true)
				}
			})
		}
	}
}

func TestBlockLogRelayKeepsLandingUnknownAndEntryUser(t *testing.T) {
	for _, entryKind := range []string{"xray", "singbox"} {
		for _, landingKind := range []string{"xray", "singbox"} {
			t.Run(entryKind+"/"+landingKind, func(t *testing.T) {
				entryLog, landingLog := &accesslog.Recorder{NodeID: 3}, &accesslog.Recorder{NodeID: 7}
				entryLog.Enable(true)
				landingLog.Enable(true)
				landing := runtimeNode(t, "vless")
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: runtimeUser(t, 1800).UUID}}
				landing.Routes = []model.RouteRule{{ID: 801, Action: "block", Ports: []string{"25"}}}
				relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"), nil, landingLog)
				entry := runtimeNode(t, "vless")
				entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
					{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort, VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}},
				}}
				entry.Routes = []model.RouteRule{{ID: 802, Action: "block", Ports: []string{"25"}}}
				user := runtimeUser(t, 1801)
				relayStartCore(t, entryKind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), nil, entryLog)
				for route, recorder := range map[int]*accesslog.Recorder{11: entryLog, 12: landingLog} {
					routed := user
					routed.UUID = relayCredential(user, route)
					client := runtimeClient(t, entry, routed)
					for _, network := range []string{"tcp", "udp"} {
						routeExpectBlocked(t, client, network, singM.ParseSocksaddr("198.51.100.10:25"), []byte(runtimePayload))
					}
					records := accessRecords(t, recorder, 2)
					if len(records) != 2 {
						t.Fatalf("重复记录: %+v", records)
					}
					for _, record := range records {
						wantUser, wantNode, wantRule := user.ID, 3, 802
						if route == 12 {
							wantUser, wantNode, wantRule = 0, 7, 801
						}
						if record.UserID != wantUser || record.NodeID != wantNode || record.RuleID != wantRule {
							t.Fatalf("中转记录归属不正确: %+v", record)
						}
					}
				}
				if batch, _ := entryLog.Next(); batch != nil {
					t.Fatal("入口重复记录落地拦截")
				}
			})
		}
	}
}
