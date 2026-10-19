package singbox

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
	"github.com/pires/go-proxyproto"
)

func TestProxyProtocolRuntime(t *testing.T) {
	for _, protocol := range []string{"shadowsocks", "shadowsocks2022-128", "shadowsocks2022-256", "vless", "trojan"} {
		for _, version := range []byte{1, 2} {
			t.Run(fmt.Sprintf("%s/v%d", protocol, version), func(t *testing.T) {
				node := runtimeNode(t, protocol)
				node.AcceptProxyProtocol = new(true)
				user := runtimeUser(t, 2401)
				core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
				core.SetDeviceIPExclude([]string{"127.0.0.1"})
				cert := kernel.TLSCert{}
				if protocol == "trojan" {
					cert = runtimeCertificate(t)
				}
				if err := core.Start(node, []model.UserSpec{user}, cert); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				forwarded := *node
				forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, version, "203.0.114.91")
				conn := runtimeDial(t, runtimeClient(t, &forwarded, user), runtimeEcho(t))
				runtimeExchange(t, conn)
				runtimeWaitTraffic(t, core, user.ID, [2]int64{int64(len(runtimePayload)), int64(len(runtimePayload))})
				_, ips, _, err := core.GetUserTraffic(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !ips[user.ID]["203.0.114.91"] || len(ips[user.ID]) != 1 {
					t.Fatalf("真实来源未进入用户设备统计: %v", ips[user.ID])
				}
			})
		}
	}
}

func TestProxyProtocolRuntimePolicyLifecycle(t *testing.T) {
	node := runtimeNode(t, "shadowsocks2022-128")
	node.AcceptProxyProtocol = new(false)
	user := runtimeUser(t, 2402)
	core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	echo := runtimeEcho(t)
	forwarded := *node
	forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.92")
	client := runtimeClient(t, &forwarded, user)
	runtimeReject(t, client, echo)
	direct := runtimeDial(t, runtimeClient(t, node, user), echo)
	runtimeExchange(t, direct)
	direct.Close()
	node.AcceptProxyProtocol = new(true)
	if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	for step := range 2 {
		invalid, changed := core.SetDeviceIPExclude([]string{"127.0.0.1"})
		if len(invalid) != 0 || !changed {
			t.Fatalf("名单更新失败: invalid=%v, changed=%v", invalid, changed)
		}
		if _, changed := core.SetDeviceIPExclude([]string{"127.0.0.1"}); changed {
			t.Fatal("重复名单不应触发更新")
		}
		instance := core.box
		conn := runtimeDial(t, client, echo)
		runtimeExchange(t, conn)
		core.SetDeviceIPExclude(nil)
		if core.box != instance {
			t.Fatal("更新统计排除名单重建了监听器")
		}
		runtimeExchange(t, conn)
		withoutList := runtimeDial(t, client, echo)
		runtimeExchange(t, withoutList)
		withoutList.Close()
		conn.Close()
		if step == 0 {
			core.Stop()
			core.SetDeviceIPExclude([]string{"127.0.0.1"})
			if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			recovered := runtimeDial(t, client, echo)
			runtimeExchange(t, recovered)
			recovered.Close()
			core.SetDeviceIPExclude(nil)
		}
	}
	core.SetDeviceIPExclude([]string{"127.0.0.1"})
	reloaded := *node
	reloaded.ServerPort = runtimeDualPort(t)
	if err := core.Reload(&reloaded, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	forwarded.ServerPort = proxyProtocolForwarder(t, reloaded.ServerPort, 1, "203.0.114.92")
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, &forwarded, user), echo))
	runtimeWaitTraffic(t, core, user.ID, [2]int64{9 * int64(len(runtimePayload)), 9 * int64(len(runtimePayload))})
}

func TestProxyProtocolRuntimeInstanceIsolation(t *testing.T) {
	user := runtimeUser(t, 2403)
	echo := runtimeEcho(t)
	for _, trusted := range []bool{true, false} {
		node := runtimeNode(t, "shadowsocks2022-128")
		node.AcceptProxyProtocol = new(trusted)
		core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
		core.SetDeviceIPExclude([]string{"127.0.0.1"})
		if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(core.Stop)
		forwarded := *node
		forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.93")
		client := runtimeClient(t, &forwarded, user)
		if trusted {
			runtimeExchange(t, runtimeDial(t, client, echo))
		} else {
			runtimeReject(t, client, echo)
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
		}
	}
}

func TestProxyProtocolRuntimeSourcePolicy(t *testing.T) {
	node := runtimeNode(t, "shadowsocks2022-128")
	node.AcceptProxyProtocol = new(true)
	node.SourcePolicy = &sourcepolicy.Policy{BlockCN: true}
	node.SourcePolicyReady = true
	node.SourceBlockCIDRs = []string{"203.0.114.94/32"}
	user := runtimeUser(t, 2404)
	core := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	core.SetDeviceIPExclude([]string{"127.0.0.1"})
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	echo := runtimeEcho(t)
	forwarded := *node
	forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.94")
	runtimeReject(t, runtimeClient(t, &forwarded, user), echo)
	forwarded.ServerPort = proxyProtocolForwarder(t, node.ServerPort, 2, "203.0.114.95")
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, &forwarded, user), echo))
}

func proxyProtocolForwarder(t *testing.T, targetPort int, version byte, source string) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var closing bool
	connections := make(map[net.Conn]bool)
	track := func(conn net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if closing {
			conn.Close()
			return false
		}
		connections[conn] = true
		return true
	}
	untrack := func(conn net.Conn) {
		conn.Close()
		mu.Lock()
		delete(connections, conn)
		mu.Unlock()
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			if !track(client) {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer untrack(client)
				upstream, err := net.Dial("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(targetPort)))
				if err != nil || !track(upstream) {
					return
				}
				defer untrack(upstream)
				header := &proxyproto.Header{
					Version: version, Command: proxyproto.PROXY, TransportProtocol: proxyproto.TCPv4,
					SourceAddr:      &net.TCPAddr{IP: net.ParseIP(source), Port: 32123},
					DestinationAddr: upstream.RemoteAddr(),
				}
				if _, err := header.WriteTo(upstream); err != nil {
					return
				}
				copied := make(chan struct{})
				go func() {
					_, _ = io.Copy(upstream, client)
					upstream.Close()
					close(copied)
				}()
				_, _ = io.Copy(client, upstream)
				client.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		closing = true
		listener.Close()
		for conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return listener.Addr().(*net.TCPAddr).Port
}
