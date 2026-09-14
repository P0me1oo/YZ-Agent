package firewall

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type recordingBackend struct {
	allows, redirects []Rule
	err               error
	closed            int
}

func (b *recordingBackend) Apply(_ context.Context, allows, redirects []Rule) error {
	b.allows, b.redirects = append([]Rule(nil), allows...), append([]Rule(nil), redirects...)
	return b.err
}

func (b *recordingBackend) Close() error { b.closed++; return nil }

func testManager(backend reconciler) *Manager {
	return &Manager{plans: map[string]Plan{}, enabled: true, linux: true, backend: backend}
}

func TestManagerSharesPortsAndReleasesLastOwner(t *testing.T) {
	ctx := context.Background()
	backend := &recordingBackend{}
	m := testManager(backend)
	node := &model.NodeSpec{Protocol: "vless", ServerPort: 8443, ListenIP: "0.0.0.0"}
	for _, owner := range []string{"first", "first", "second"} {
		if err := m.Apply(ctx, owner, node, "xray"); err != nil {
			t.Fatal(err)
		}
		if len(backend.allows) != 1 {
			t.Fatal("重复调用或共享端口不能累加规则")
		}
	}
	if err := m.Release(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if len(backend.allows) != 1 {
		t.Fatal("不能关闭另一个运行节点所需的端口")
	}
	if err := m.Release(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if len(backend.allows) != 0 {
		t.Fatal("最后一个节点停止后应清理规则")
	}
	if err := m.Release(ctx, "second"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerReplacesRangesAndRejectsConflicts(t *testing.T) {
	ctx := context.Background()
	backend := &recordingBackend{}
	m := testManager(backend)
	node := &model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 8443, ListenIP: "0.0.0.0", PortHopping: "20000-20010,20020"}
	if err := m.Apply(ctx, "first", node, "xray"); err != nil {
		t.Fatal(err)
	}
	before := append([]Rule(nil), backend.redirects...)
	conflict := *node
	conflict.ServerPort, conflict.PortHopping = 9443, "20005-20015"
	if err := m.Apply(ctx, "second", &conflict, "singbox"); err == nil {
		t.Fatal("重叠范围不能指向不同节点")
	}
	if !reflect.DeepEqual(before, backend.redirects) {
		t.Fatal("冲突不能改动健康节点的规则")
	}
	conflict.Protocol, conflict.PortHopping, conflict.ServerPort = "tuic", "", 20006
	if err := m.Apply(ctx, "second", &conflict, "singbox"); err == nil {
		t.Fatal("跳跃范围不能占用其他 UDP 节点的真实端口")
	}
	node.PortHopping = "20100-20110"
	if err := m.Apply(ctx, "first", node, "xray"); err != nil {
		t.Fatal(err)
	}
	if len(backend.redirects) != 1 || backend.redirects[0].Ports.From != 20100 {
		t.Fatal("端口范围重载后仍有旧规则")
	}
}

func TestManagerCleanupRetriesAfterFailure(t *testing.T) {
	ctx := context.Background()
	backend := &recordingBackend{}
	m := testManager(backend)
	node := &model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 8443, PortHopping: "20000-20010"}
	if err := m.Apply(ctx, "node", node, "singbox"); err != nil {
		t.Fatal(err)
	}
	backend.err = errors.New("防火墙暂不可用")
	if err := m.Release(ctx, "node"); err == nil {
		t.Fatal("清理失败不能报告成功")
	}
	backend.err = nil
	if err := m.Release(ctx, "node"); err != nil {
		t.Fatal(err)
	}
	if len(backend.allows)+len(backend.redirects) != 0 {
		t.Fatal("重试后仍有已停节点的规则")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.closed != 1 {
		t.Fatal("重复停止只能关闭后端一次")
	}
}

func TestManagerConcurrentApplyReleaseAndClose(t *testing.T) {
	backend := &recordingBackend{}
	m := testManager(backend)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for _, owner := range []string{"first", "second", "third"} {
		workers.Go(func() {
			<-start
			node := &model.NodeSpec{Protocol: "vless", ServerPort: 8443}
			for range 20 {
				if m.Apply(ctx, owner, node, "xray") != nil {
					return
				}
				if err := m.Release(ctx, owner); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for range 2 {
		workers.Go(func() {
			<-start
			if err := m.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	workers.Wait()
	if len(backend.allows)+len(backend.redirects) != 0 || backend.closed != 1 {
		t.Fatal("并发退出后仍有规则或后端重复关闭")
	}
}

func TestPlanFollowsKernelTransportAndRelayListener(t *testing.T) {
	for _, test := range []struct {
		name, kernel string
		node         model.NodeSpec
		protocols    []string
		port         int
	}{
		{"HY2", "xray", model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 8443}, []string{"udp"}, 8443},
		{"VLESS KCP", "xray", model.NodeSpec{Protocol: "vless", Network: "kcp", ServerPort: 8443}, []string{"udp"}, 8443},
		{"VLESS WS", "singbox", model.NodeSpec{Protocol: "vless", Network: "ws", ServerPort: 8443}, []string{"tcp"}, 8443},
		{"SS", "xray", model.NodeSpec{Protocol: "shadowsocks", ServerPort: 8443}, []string{"tcp", "udp"}, 8443},
		{"Mieru UDP", "singbox", model.NodeSpec{Protocol: "mieru", Transport: "UDP", ServerPort: 8443}, []string{"udp"}, 8443},
		{"落地", "xray", model.NodeSpec{Protocol: "vless", ServerPort: 443, Relay: &model.RelayConfig{Mode: "landing", Protocol: "shadowsocks", ListenPort: 28388}}, []string{"tcp", "udp"}, 28388},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.node.ListenIP = "192.0.2.10"
			plan, err := PlanForNode(&test.node, test.kernel)
			if err != nil {
				t.Fatal(err)
			}
			var protocols []string
			for _, rule := range plan.Listeners {
				protocols = append(protocols, rule.Protocol)
				if rule.Address != "192.0.2.10" || rule.Family != 4 || rule.Ports.From != test.port {
					t.Fatalf("监听范围错误：%+v", rule)
				}
			}
			if !reflect.DeepEqual(protocols, test.protocols) {
				t.Fatalf("协议错误：%v", protocols)
			}
		})
	}
}

func TestPlanCoversIPv6AndExcludesListeningPort(t *testing.T) {
	node := &model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 20005, PortHopping: "20000-20010", ListenIP: "::"}
	plan, err := PlanForNode(node, "xray")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Listeners) != 2 || len(plan.Redirects) != 4 {
		t.Fatalf("双栈范围错误：%+v", plan)
	}
	for _, rule := range plan.Redirects {
		if rule.Ports.Contains(node.ServerPort) {
			t.Fatal("不应重定向真实监听端口")
		}
	}
	node.ListenIP = "2001:db8::10"
	plan, err = PlanForNode(node, "singbox")
	if err != nil || len(plan.Listeners) != 1 || plan.Listeners[0].Family != 6 {
		t.Fatalf("IPv6 专用监听错误：%+v, %v", plan, err)
	}
}
