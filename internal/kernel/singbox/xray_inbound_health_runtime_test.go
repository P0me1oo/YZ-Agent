//go:build with_quic

package singbox

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	singM "github.com/sagernet/sing/common/metadata"
)

// 使用实际的 Xray VLESS 前置和 sing-box SS 落地，验证健康闲置、重载和关闭回收。
func TestXrayVLESSHealthWithSingboxSSLanding(t *testing.T) {
	landing := runtimeNode(t, "shadowsocks")
	landing.Relay = &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", Cipher: "2022-blake3-aes-128-gcm",
		Password: base64.StdEncoding.EncodeToString([]byte(runtimeUser(t, 951).UUID)[:16])}
	relayStartCore(t, "singbox", landing, nil, kernel.TLSCert{}, relayMarkerEcho(t, "health"))
	node := runtimeNode(t, "vless")
	node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{
		NodeID: 7, Tag: "relay-7", RouteID: 12, Protocol: "shadowsocks", Address: "127.0.0.1", Port: landing.ServerPort,
		Cipher: landing.Relay.Cipher, Password: landing.Relay.Password,
	}}}
	user := runtimeUser(t, 952)
	user.DeviceLimit = 1
	entry := relayStartCore(t, "xray", node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "direct"))
	clientUser := user
	clientUser.UUID = relayCredential(user, 12)
	connect := func(current *model.NodeSpec) net.Conn {
		conn := runtimeDial(t, runtimeClient(t, current, clientUser), singM.ParseSocksaddr("198.51.100.10:80"))
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	exchange := func(conn net.Conn) {
		t.Helper()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, runtimePayload); err != nil {
			t.Fatal(err)
		}
		want := "health:" + runtimePayload
		got := make([]byte, len(want))
		if _, err := io.ReadFull(conn, got); err != nil || string(got) != want {
			t.Fatalf("中转应答不正确：error=%v，长度=%d", err, len(got))
		}
		if err := conn.SetDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	oldConn := connect(node)
	exchange(oldConn)
	t.Log("保持健康连接闲置 65 秒，确认探测不会把正常待机当作离线")
	time.Sleep(65 * time.Second)
	exchange(oldConn)
	changed := *node
	changed.ServerPort = runtimeNode(t, "vless").ServerPort
	for range 2 {
		if err := entry.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
	}
	exchange(oldConn)
	newConn := connect(&changed)
	exchange(newConn)
	_ = oldConn.Close()
	_ = newConn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, alive, count, err := entry.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(alive[user.ID]) == 0 && count == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("旧连接全部关闭后，入口在线来源或连接数未归零")
		}
		time.Sleep(20 * time.Millisecond)
	}
	exchange(connect(&changed))
	traffic, _, _, err := entry.GetUserTraffic(context.Background())
	if err != nil || traffic[user.ID][0] <= 0 || traffic[user.ID][1] <= 0 {
		t.Fatal("入口流量累计丢失")
	}
}
