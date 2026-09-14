package singbox

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	xrayKernel "github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

func TestSingBoxReloadRemovesPreviousProtocolListener(t *testing.T) {
	node := runtimeNode(t, "socks")
	user := runtimeUser(t, 601)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	echo := runtimeEcho(t)
	oldConn := runtimeDial(t, runtimeClient(t, node, user), echo)
	runtimeExchange(t, oldConn)
	_ = oldConn.Close()
	changed := runtimeNode(t, "http")
	for range 2 {
		if err := s.Reload(changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
		assertListenerClosed(t, node)
		if _, exists := s.box.Inbound().Get("socks-in"); exists {
			t.Fatal("旧协议入站仍存在于运行实例")
		}
		conn := runtimeDial(t, runtimeClient(t, changed, user), echo)
		runtimeExchange(t, conn)
		_ = conn.Close()
	}
	if _, err := s.RemoveUsers([]model.UserSpec{user}); err != nil {
		t.Fatal(err)
	}
	runtimeReject(t, runtimeClient(t, changed, user), echo)
	assertListenerClosed(t, node)
}

func TestSingBoxReloadStopsAfterBindFailure(t *testing.T) {
	node := runtimeNode(t, "socks")
	oldUser, newUser := runtimeUser(t, 602), runtimeUser(t, 603)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	if err := s.Start(node, []model.UserSpec{oldUser}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	echo := runtimeEcho(t)
	conn := runtimeDial(t, runtimeClient(t, node, oldUser), echo)
	runtimeExchange(t, conn)
	_ = conn.Close()
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	changed := *node
	changed.ServerPort = occupied.Addr().(*net.TCPAddr).Port
	changed.CustomRoutes = []map[string]any{{"ip_cidr": []string{"127.0.0.1/32"}, "action": "reject"}}
	for _, apply := range []func(*model.NodeSpec, []model.UserSpec, kernel.TLSCert) error{s.Reload, s.Start} {
		if err := apply(&changed, []model.UserSpec{newUser}, kernel.TLSCert{}); err == nil {
			t.Fatal("被占用的端口不应启动成功")
		}
		if s.IsRunning() {
			t.Fatal("新配置失败后内核仍在运行")
		}
		assertListenerClosed(t, node)
		runtimeReject(t, runtimeClient(t, node, newUser), echo)
	}
	_ = occupied.Close()
	if err := s.Start(&changed, []model.UserSpec{newUser}, kernel.TLSCert{}); err != nil {
		t.Fatalf("释放端口后同一配置仍不能重试: %v", err)
	}
	assertListenerClosed(t, node)
	runtimeReject(t, runtimeClient(t, &changed, newUser), echo)
}

func TestSingBoxReloadAppliesOutboundChanges(t *testing.T) {
	for _, newTag := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_tag", true: "new_tag"}[newTag], func(t *testing.T) {
			node := runtimeNode(t, "socks")
			node.CustomOutbounds = []model.OutboundConfig{{Tag: "test-egress", Protocol: "direct"}}
			node.CustomRoutes = []map[string]any{{"ip_cidr": []string{"127.0.0.1/32"}, "outbound": "test-egress"}}
			user := runtimeUser(t, 604)
			s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
			t.Cleanup(s.Stop)
			if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			echo := runtimeEcho(t)
			conn := runtimeDial(t, runtimeClient(t, node, user), echo)
			runtimeExchange(t, conn)
			_ = conn.Close()
			changed := *node
			tag := "test-egress"
			if newTag {
				tag = "test-new-egress"
			}
			changed.CustomOutbounds = []model.OutboundConfig{{Tag: tag, Protocol: "block"}}
			changed.CustomRoutes = []map[string]any{{"ip_cidr": []string{"127.0.0.1/32"}, "outbound": tag}}
			for range 2 {
				if err := s.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				outbound, exists := s.box.Outbound().Outbound(tag)
				if !exists || outbound.Type() != "block" {
					t.Fatal("返回重载成功，但实际出站没有更新")
				}
				if newTag {
					if _, exists := s.box.Outbound().Outbound("test-egress"); exists {
						t.Fatal("已删除的出站仍然存在")
					}
				}
				runtimeReject(t, runtimeClient(t, &changed, user), echo)
			}
			if err := s.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
				t.Fatal(err)
			}
			runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
		})
	}
}

func TestSingBoxFailedRebuildLeavesKernelStoppedAndRecoverable(t *testing.T) {
	node := runtimeNode(t, "socks")
	user := runtimeUser(t, 608)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	// 模拟旧监听释放后端口被抢占，使候选配置和回滚配置都无法绑定。
	if err := s.box.Inbound().Close(); err != nil {
		t.Fatal(err)
	}
	oldPort, err := net.Listen("tcp4", net.JoinHostPort(node.ListenIP, strconv.Itoa(node.ServerPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer oldPort.Close()
	newPort, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer newPort.Close()
	changed := *node
	changed.ServerPort = newPort.Addr().(*net.TCPAddr).Port
	if err := s.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err == nil || s.IsRunning() {
		t.Fatalf("回滚也失败时不能仍报告正在运行: %v", err)
	}
	_ = oldPort.Close()
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatalf("释放旧端口后恢复失败: %v", err)
	}
	echo := runtimeEcho(t)
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
}

func TestSingBoxRestartPreservesGlobalDeviceLimits(t *testing.T) {
	node := runtimeNode(t, "socks")
	user := runtimeUser(t, 610)
	s := New(config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: t.TempDir()})
	t.Cleanup(s.Stop)
	s.SetDeviceLimitFunc(func(string) (int, bool) { return 1, true })
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	s.UpdateGlobalDevices(map[int][]string{user.ID: {"0.0.0.1"}})
	if err := s.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	echo := runtimeEcho(t)
	runtimeReject(t, runtimeClient(t, node, user), echo)
	s.ClearGlobalDevices()
	runtimeExchange(t, runtimeDial(t, runtimeClient(t, node, user), echo))
}

func TestXrayFailedRestartStopsKernelAndClosesPreviousListener(t *testing.T) {
	node := runtimeNode(t, "vless")
	allowXrayLoopback(node)
	user := runtimeUser(t, 609)
	k := xrayKernel.New(config.KernelConfig{Type: "xray", LogLevel: "none"})
	t.Cleanup(k.Stop)
	if err := k.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	changed := *node
	changed.ServerPort = occupied.Addr().(*net.TCPAddr).Port
	if err := k.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err == nil {
		t.Fatal("端口占用时应返回重载失败")
	}
	if k.IsRunning() {
		t.Fatal("失败候选实例后内核仍报告运行")
	}
	assertListenerClosed(t, node)
}

func TestKernelTrafficSurvivesRestart(t *testing.T) {
	for _, kernelType := range []string{"singbox", "xray"} {
		for _, restart := range []string{"stop_start", "reload", "start"} {
			t.Run(kernelType+"/"+restart, func(t *testing.T) {
				node := runtimeNode(t, "vless")
				cfg := config.KernelConfig{Type: kernelType, LogLevel: "none", ConfigDir: t.TempDir()}
				var k kernel.Kernel
				if kernelType == "xray" {
					allowXrayLoopback(node)
					k = xrayKernel.New(cfg)
				} else {
					cfg.LogLevel = "fatal"
					k = New(cfg)
				}
				t.Cleanup(k.Stop)
				user := runtimeUser(t, 605)
				if err := k.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				echo := runtimeEcho(t)
				conn := runtimeDial(t, runtimeClient(t, node, user), echo)
				runtimeExchange(t, conn)
				_ = conn.Close()
				unit := int64(len(runtimePayload))
				waitKernelTraffic(t, k, user.ID, [2]int64{unit, unit})
				accounting := tracker.New()
				traffic, ips, count, _ := k.GetUserTraffic(context.Background())
				accounting.Process(traffic, ips, count)
				changed := *node
				switch restart {
				case "stop_start":
					k.Stop()
					waitKernelTraffic(t, k, user.ID, [2]int64{unit, unit})
					if err := k.Start(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
				case "reload":
					changed.ServerPort = runtimeNode(t, "vless").ServerPort
					if err := k.Reload(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
				case "start":
					if err := k.Start(&changed, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
						t.Fatal(err)
					}
				}
				conn = runtimeDial(t, runtimeClient(t, &changed, user), echo)
				runtimeExchange(t, conn)
				runtimeExchange(t, conn)
				_ = conn.Close()
				waitKernelTraffic(t, k, user.ID, [2]int64{3 * unit, 3 * unit})
				for range 2 {
					traffic, ips, count, _ = k.GetUserTraffic(context.Background())
					accounting.Process(traffic, ips, count)
				}
				if got := accounting.FlushTraffic()[user.ID]; got != [2]int64{3 * unit, 3 * unit} {
					t.Fatalf("跨重启结算流量不正确: %v", got)
				}
				k.Stop()
				waitKernelTraffic(t, k, user.ID, [2]int64{3 * unit, 3 * unit})
			})
		}
	}
}

func TestXrayReloadClosesOldListenerAndCountsDrainingUser(t *testing.T) {
	node := runtimeNode(t, "vless")
	allowXrayLoopback(node)
	k := xrayKernel.New(config.KernelConfig{Type: "xray", LogLevel: "none"})
	t.Cleanup(k.Stop)
	oldUser, newUser := runtimeUser(t, 606), runtimeUser(t, 607)
	if err := k.Start(node, []model.UserSpec{oldUser}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	echo := runtimeEcho(t)
	oldConn := runtimeDial(t, runtimeClient(t, node, oldUser), echo)
	runtimeExchange(t, oldConn)
	unit := int64(len(runtimePayload))
	waitKernelTraffic(t, k, oldUser.ID, [2]int64{unit, unit})
	changed := *node
	changed.ServerPort = runtimeNode(t, "vless").ServerPort
	for range 2 {
		if err := k.Reload(&changed, []model.UserSpec{newUser}, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
		assertListenerClosed(t, node)
	}
	runtimeExchange(t, oldConn)
	newConn := runtimeDial(t, runtimeClient(t, &changed, newUser), echo)
	runtimeExchange(t, newConn)
	runtimeReject(t, runtimeClient(t, &changed, oldUser), echo)
	waitKernelTraffic(t, k, oldUser.ID, [2]int64{2 * unit, 2 * unit})
	waitKernelTraffic(t, k, newUser.ID, [2]int64{unit, unit})
	_ = oldConn.Close()
	_ = newConn.Close()
	k.Stop()
	assertListenerClosed(t, &changed)
	waitKernelTraffic(t, k, oldUser.ID, [2]int64{2 * unit, 2 * unit})
	waitKernelTraffic(t, k, newUser.ID, [2]int64{unit, unit})
}

func allowXrayLoopback(node *model.NodeSpec) {
	// Xray 的路由和 freedom 出站各自校验私网目标，隔离回环测试需要同时允许。
	node.CustomRoutes = []map[string]any{{"type": "field", "ip": []string{"127.0.0.1/32"}, "outboundTag": "direct"}}
	node.CustomOutbounds = []model.OutboundConfig{{Tag: "direct", Protocol: "direct", Settings: map[string]any{
		"finalRules": []map[string]any{{"action": "allow", "ip": []string{"127.0.0.1/32"}}},
	}}}
}

func assertListenerClosed(t *testing.T, node *model.NodeSpec) {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort(node.ListenIP, strconv.Itoa(node.ServerPort)), 300*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatal("退出配置的旧监听仍然接受新连接")
	}
}

func waitKernelTraffic(t *testing.T, k kernel.Kernel, userID int, want [2]int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		traffic, _, _, err := k.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := traffic[userID]
		if got == want {
			return
		}
		if got[0] > want[0] || got[1] > want[1] || time.Now().After(deadline) {
			t.Fatalf("%s 累计流量: got=%v, want=%v", k.Name(), got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
