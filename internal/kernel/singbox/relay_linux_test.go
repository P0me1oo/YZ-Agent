//go:build linux

package singbox

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"golang.org/x/sys/unix"
)

func TestRelayShadowsocksListenerAllowsUDPFragment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		listenIP string
		level    int
		option   int
	}{
		{name: "IPv4", listenIP: "127.0.0.1", level: unix.IPPROTO_IP, option: unix.IP_MTU_DISCOVER},
		{name: "IPv6", listenIP: "::1", level: unix.IPPROTO_IPV6, option: unix.IPV6_MTU_DISCOVER},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &model.NodeSpec{
				Protocol: "shadowsocks",
				ListenIP: tc.listenIP,
				Relay: &model.RelayConfig{
					Mode:     "landing",
					Protocol: "shadowsocks",
					Cipher:   "aes-128-gcm",
					Password: runtimeUser(t, 805).UUID,
				},
			}
			cfg, err := buildConfig(config.KernelConfig{}, node, nil, kernel.TLSCert{})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(cfg["inbounds"].([]M)[0])
			if err != nil {
				t.Fatal(err)
			}
			var options option.ShadowsocksInboundOptions
			if err := json.Unmarshal(encoded, &options); err != nil {
				t.Fatal(err)
			}
			inbound := listener.New(listener.Options{
				Context: context.Background(),
				Logger:  log.NewNOPFactory().Logger(),
				Listen:  options.ListenOptions,
			})
			conn, err := inbound.ListenUDP()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			raw, err := conn.(*net.UDPConn).SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var mode int
			var socketErr error
			if err := raw.Control(func(fd uintptr) {
				mode, socketErr = unix.GetsockoptInt(int(fd), tc.level, tc.option)
			}); err != nil {
				t.Fatal(err)
			}
			if socketErr != nil {
				t.Fatal(socketErr)
			}
			if mode == unix.IP_PMTUDISC_DO || mode == unix.IP_PMTUDISC_PROBE {
				t.Fatalf("Shadowsocks 中转监听仍禁止 UDP 分片: MTU discovery mode=%d", mode)
			}
		})
	}
}
