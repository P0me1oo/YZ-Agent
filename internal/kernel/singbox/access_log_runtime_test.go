//go:build with_quic

package singbox

import (
	"encoding/base64"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
	"testing"
	"time"
)

func accessRecords(t *testing.T, r *accesslog.Recorder, want int) []accesslog.Record {
	t.Helper()
	var result []accesslog.Record
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if batch != nil {
			result = append(result, batch.Records...)
			r.Ack(batch.ID)
		}
		if len(result) >= want {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("得到 %d 条记录，期望 %d 条", len(result), want)
	return nil
}

func TestAccessLogRelayActualLanding(t *testing.T) {
	for _, entryKind := range []string{"singbox", "xray"} {
		for _, landingKind := range []string{"singbox", "xray"} {
			for _, protocol := range []string{"vless", "hysteria2"} {
				t.Run(entryKind+"/"+landingKind+"/"+protocol, func(t *testing.T) {
					entryLog, landingLog := &accesslog.Recorder{NodeID: 3}, &accesslog.Recorder{NodeID: 7}
					entryLog.Enable(true)
					landingLog.Enable(true)
					user := runtimeUser(t, 945)
					landing := runtimeNode(t, "vless")
					landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: runtimeUser(t, 946).UUID}}
					relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"), landingLog)
					ss := runtimeNode(t, "shadowsocks")
					ss.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
						Password: base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 947).UUID)[:16])}
					relayStartCore(t, landingKind, ss, nil, kernel.TLSCert{}, relayMarkerEcho(t, "ss"), landingLog)
					entry := runtimeNode(t, protocol)
					entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
						{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort, VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}},
						{NodeID: 9, Tag: "relay-9", RouteID: 13, Protocol: "shadowsocks", Address: "127.0.0.1", Port: ss.ServerPort, Cipher: ss.Relay.Cipher, Password: ss.Relay.Password},
					}}
					cert := runtimeCertificate(t)
					core := relayStartCore(t, entryKind, entry, []model.UserSpec{user}, cert, relayMarkerEcho(t, "direct"), entryLog)
					for run := 0; run < 2; run++ {
						if run > 0 {
							if err := core.Reload(entry, []model.UserSpec{user}, cert); err != nil {
								t.Fatal(err)
							}
						}
						for route, marker := range map[int]string{11: "direct", 12: "landing", 13: "ss"} {
							routed := user
							routed.UUID = relayCredential(user, route)
							client := runtimeClient(t, entry, routed)
							for _, network := range []string{"tcp", "udp"} {
								relayMarkerExchange(t, client, marker, network, singM.ParseSocksaddr("198.51.100.10:443"))
							}
						}
						records := accessRecords(t, entryLog, 6)
						counts := map[int]int{}
						for _, record := range records {
							if record.UserID != user.ID || record.Destination != "198.51.100.10" || record.Port != 443 {
								t.Fatalf("中转记录身份或目标不正确：%+v", record)
							}
							counts[record.NodeID]++
						}
						if len(records) != 6 || counts[3] != 2 || counts[7] != 2 || counts[9] != 2 {
							t.Fatalf("未按实际出口记录一次：%v", counts)
						}
					}
					if batch, _ := landingLog.Next(); batch != nil {
						t.Fatal("中转落地重复记录")
					}
				})
			}
		}
	}
}

func TestAccessLogCustomRouteUsesActualOutbound(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			r := &accesslog.Recorder{NodeID: 3}
			r.Enable(true)
			user := runtimeUser(t, 948)
			landing := runtimeNode(t, "vless")
			landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: runtimeUser(t, 949).UUID}}
			relayStartCore(t, kind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"))
			entry := runtimeNode(t, "vless")
			entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
				{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort, VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}},
			}}
			// 身份仍选择落地，但管理员路由强制直出，记录应归属入口自身。
			entry.CustomRouteRules = []model.CustomRouteRule{{
				Match: model.RouteMatch{IPCIDRs: []string{"198.51.100.10/32"}}, Action: model.RouteAction{Type: "direct"},
			}}
			relayStartCore(t, kind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), r)
			user.UUID = relayCredential(user, 12)
			client := runtimeClient(t, entry, user)
			for _, network := range []string{"tcp", "udp"} {
				relayMarkerExchange(t, client, "direct", network, singM.ParseSocksaddr("198.51.100.10:443"))
			}
			records := accessRecords(t, r, 2)
			if len(records) != 2 {
				t.Fatal("改选出口后重复记录")
			}
			for _, record := range records {
				if record.NodeID != 3 || record.UserID != user.ID || record.Destination != "198.51.100.10" || record.Port != 443 {
					t.Fatal("错误使用了原计划落地")
				}
			}
		})
	}
}

func TestAccessLogBothCoresTCPUDPAndReload(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"vless", "shadowsocks", "trojan"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				r := &accesslog.Recorder{}
				r.Enable(true)
				node, user, cert := runtimeNode(t, protocol), runtimeUser(t, 941), runtimeCertificate(t)
				core := relayStartCore(t, kind, node, []model.UserSpec{user}, cert, relayMarkerEcho(t, "access"), r)
				for run := 0; run < 2; run++ {
					if run > 0 {
						if err := core.Reload(node, []model.UserSpec{user}, cert); err != nil {
							t.Fatal(err)
						}
					}
					var tlsOptions map[string]any
					if kind == "xray" && protocol == "trojan" {
						tlsOptions = map[string]any{"enabled": true, "insecure": true, "server_name": "localhost", "alpn": []string{"http/1.1"}}
					}
					client := runtimeClientWithTLS(t, node, user, tlsOptions)
					started := time.Now().Unix()
					for _, host := range []string{"example.test", "198.51.100.10"} {
						for _, network := range []string{"tcp", "udp"} {
							relayMarkerExchange(t, client, "access", network, singM.ParseSocksaddr(host+":443"))
						}
					}
					records := accessRecords(t, r, 4)
					if len(records) != 4 {
						t.Fatalf("单次连接重复记录：%d", len(records))
					}
					counts := map[string]int{}
					for _, record := range records {
						if record.UserID != user.ID || record.Port != 443 || record.ConnectedAt < started {
							t.Fatal("记录身份、端口或连接时间错误")
						}
						counts[record.Destination]++
					}
					if counts["example.test"] != 2 || counts["198.51.100.10"] != 2 {
						t.Fatalf("目标信息错误：%v", counts)
					}
				}
			})
		}
	}
}
