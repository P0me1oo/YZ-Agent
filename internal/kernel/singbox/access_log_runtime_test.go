//go:build with_quic

package singbox

import (
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

func TestAccessLogRelayEntryOnly(t *testing.T) {
	for _, entryKind := range []string{"singbox", "xray"} {
		for _, landingKind := range []string{"singbox", "xray"} {
			t.Run(entryKind+"/"+landingKind, func(t *testing.T) {
				entryLog, landingLog := &accesslog.Recorder{}, &accesslog.Recorder{}
				entryLog.Enable(true)
				landingLog.Enable(true)
				user := runtimeUser(t, 945)
				landing := runtimeNode(t, "vless")
				landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "vless", VLESS: &model.RelayVLESSConfig{ID: runtimeUser(t, 946).UUID}}
				relayStartCore(t, landingKind, landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "landing"), landingLog)
				entry := runtimeNode(t, "vless")
				entry.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "vless", Address: "127.0.0.1", Port: landing.ServerPort, VLESS: &model.RelayVLESSConfig{ID: landing.Relay.VLESS.ID, Network: "tcp", Encryption: "none"}}}}
				relayStartCore(t, entryKind, entry, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"), entryLog)
				routed := user
				routed.UUID = relayCredential(user, 12)
				client := runtimeClient(t, entry, routed)
				for _, network := range []string{"tcp", "udp"} {
					relayMarkerExchange(t, client, "landing", network)
				}
				records := accessRecords(t, entryLog, 2)
				if len(records) != 2 || records[0].UserID != user.ID || records[0].Destination != "198.51.100.10" {
					t.Fatal("中转入口记录不正确")
				}
				if batch, _ := landingLog.Next(); batch != nil {
					t.Fatal("中转落地重复记录")
				}
			})
		}
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
