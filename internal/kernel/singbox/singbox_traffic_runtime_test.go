//go:build with_wireguard

package singbox

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/directwg"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func TestProtocolSwitchKeepsUnreportedTraffic(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			node := runtimeNode(t, "vless")
			user := runtimeUser(t, 9301)
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			peer, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			user.WireGuard = &panel.WireGuardPeer{
				PublicKey: base64.StdEncoding.EncodeToString(peer.PublicKey().Bytes()), Address: []string{"10.123.0.2/32"},
			}
			base := relayStartCore(
				t,
				kind,
				node,
				[]model.UserSpec{user},
				kernel.TLSCert{},
				relayMarkerEcho(t, "direct"),
			)
			managed := directwg.Wrap(base)
			t.Cleanup(managed.Stop)
			client := runtimeClient(t, node, user)
			relayMarkerExchange(
				t,
				client,
				"direct",
				"tcp",
			)
			unit := int64(len(runtimePayload))
			waitKernelTraffic(
				t,
				managed,
				user.ID,
				[2]int64{unit, unit + int64(len("direct:"))},
			)
			before, _, _, err := managed.GetUserTraffic(context.Background())
			if err != nil || before[user.ID][0] == 0 || before[user.ID][1] == 0 {
				t.Fatalf("切换前未取得真实流量: %v %v", before, err)
			}
			next := *node
			next.Protocol = "wireguard"
			next.WireGuard = &panel.WireGuardConfig{
				PrivateKey: base64.StdEncoding.EncodeToString(key.Bytes()), Address: []string{"10.123.0.1/32"}, MTU: 1420,
			}
			for range 2 {
				if err := managed.Reload(&next, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				traffic, _, _, err := managed.GetUserTraffic(context.Background())
				if err != nil || traffic[user.ID] != before[user.ID] {
					t.Fatalf("切换到 WG 隐藏旧协议累计: before=%v after=%v err=%v", before, traffic, err)
				}
			}
			if err := managed.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			traffic, _, _, err := managed.GetUserTraffic(context.Background())
			if err != nil || traffic[user.ID] != before[user.ID] {
				t.Fatalf("切回代理协议重复计费或丢失累计: before=%v after=%v err=%v", before, traffic, err)
			}
		})
	}
}
