//go:build with_quic

package singbox

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	singM "github.com/sagernet/sing/common/metadata"
)

func TestNextTrustedProxyRuntimeBothCores(t *testing.T) {
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			node := runtimeNode(t, "vless")
			node.AccessPolicyVersion = 1
			node.AcceptProxyProtocol = new(true)
			node.TrustedProxyIPs = []string{"127.0.0.1"}
			user := runtimeUser(t, 7801)
			target := runtimeEcho(t)
			echo := singM.ParseSocksaddr("198.51.100.10:80")
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, target.String())
			for _, version := range []byte{1, 2} {
				forwarded := *node
				forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, version, "8.8.4.4")
				client := proxySwitchClient(t, &forwarded, user)
				conn := runtimeDial(t, client, echo)
				runtimeExchange(t, conn)
				_, ips, _, err := core.GetUserTraffic(context.Background())
				if err != nil || !ips[user.ID]["8.8.4.4"] {
					t.Fatal("trusted source not identified")
				}
				conn.Close()
				changed := *node
				changed.TrustedProxyIPs = []string{"192.0.2.9"}
				if err := core.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				runtimeReject(t, client, echo)
				runtimeExchange(t, runtimeDial(t, proxySwitchClient(t, node, user), echo))
				if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				runtimeExchange(t, runtimeDial(t, client, echo))
			}
		})
	}
}

// Consume actual Next snapshots, not handwritten copies of its wire format.
func TestNextExportedPolicySnapshots(t *testing.T) {
	dir := os.Getenv("TEST_POLICY_EXPORT")
	if dir == "" {
		t.Skip("requires Next policy export")
	}
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, kind+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var wire panel.NodeConfig
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			node, err := model.NodeSpecFromPanelValidated(&wire, config.KernelConfig{Type: kind})
			if err != nil {
				t.Fatal(err)
			}
			if len(node.Routes) != 2 || len(node.CustomOutbounds) != 1 {
				t.Fatal("snapshot policy missing")
			}
			node.ServerPort = runtimeDualPort(t)
			node.ListenIP = "127.0.0.1"
			user := runtimeUser(t, 7802)
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, runtimeEcho(t).String())
			if !core.IsRunning() {
				t.Fatal("core not running")
			}
		})
	}
}

func TestNextConfiguredExitBlockAndRemovalRuntime(t *testing.T) {
	dir := os.Getenv("TEST_POLICY_EXPORT")
	if dir == "" {
		t.Skip("requires Next policy export")
	}
	for _, kind := range []string{"singbox", "xray"} {
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, kind+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var wire panel.NodeConfig
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			node := model.NodeSpecFromPanel(&wire)
			node.ServerPort = runtimeDualPort(t)
			node.ListenIP = "127.0.0.1"
			// A real local SOCKS exit has a distinguishable response from the default exit.
			exit := runtimeNode(t, "socks")
			exitUser := runtimeUser(t, 7803)
			marker := relayMarkerEcho(t, "chosen")
			_, portText, _ := net.SplitHostPort(marker)
			port, _ := strconv.Atoi(portText)
			exit.CustomRoutes = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": port}}
			exitCore := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			if err := exitCore.Start(exit, []model.UserSpec{exitUser}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(exitCore.Stop)
			settings := node.CustomOutbounds[0].Settings
			if kind == "singbox" {
				settings["server_port"] = exit.ServerPort
				settings["username"], settings["password"] = exitUser.UUID, exitUser.UUID
			} else {
				settings["servers"] = []map[string]any{{"address": "127.0.0.1", "port": exit.ServerPort, "users": []map[string]any{{"user": exitUser.UUID, "pass": exitUser.UUID}}}}
			}
			user := runtimeUser(t, 7804)
			core := relayStartCore(t, kind, node, []model.UserSpec{user}, kernel.TLSCert{}, relayMarkerEcho(t, "default"))
			client := routeTestClient(t, node, user)
			target := singM.ParseSocksaddr("sub.example.com:80")
			routeExpectPassMarker(t, client, "tcp", target, []byte(runtimePayload), "chosen")
			blocked := singM.ParseSocksaddr("speedtest.net:80")
			blockedConn, err := runtimeConnect(t.Context(), client, blocked)
			if err == nil {
				blockedConn.SetDeadline(time.Now().Add(time.Second))
				_, _ = blockedConn.Write([]byte(runtimePayload))
				var reply [128]byte
				n, err := blockedConn.Read(reply[:])
				blockedConn.Close()
				if n > 0 || err == nil {
					t.Fatalf("blocked target received application response: %q", reply[:n])
				}
			}
			changed := *node
			changed.Routes = nil
			changed.CustomOutbounds = nil
			if err := core.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			routeExpectPassMarker(t, client, "tcp", singM.ParseSocksaddr("198.51.100.10:80"), []byte(runtimePayload), "default")
		})
	}
}

func TestNextAllConfiguredExitProtocolsStartBothCores(t *testing.T) {
	dir := os.Getenv("TEST_POLICY_EXPORT")
	if dir == "" {
		t.Skip("requires Next policy export")
	}
	for _, kind := range []string{"singbox", "xray"} {
		for _, protocol := range []string{"socks", "http", "shadowsocks"} {
			t.Run(kind+"/"+protocol, func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join(dir, kind+"-"+protocol+".json"))
				if err != nil {
					t.Fatal(err)
				}
				var wire panel.NodeConfig
				if err = json.Unmarshal(raw, &wire); err != nil {
					t.Fatal(err)
				}
				node, err := model.NodeSpecFromPanelValidated(&wire, config.KernelConfig{Type: kind})
				if err != nil {
					t.Fatal(err)
				}
				node.ServerPort = runtimeDualPort(t)
				node.ListenIP = "127.0.0.1"
				core := relayStartCore(t, kind, node, []model.UserSpec{runtimeUser(t, 7820)}, kernel.TLSCert{}, runtimeEcho(t).String())
				if !core.IsRunning() {
					t.Fatal("exit configuration failed to start")
				}
			})
		}
	}
}
