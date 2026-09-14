//go:build linux

package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

// 原生命令测试只能在独立 rootfs、网络命名空间和专用客户端中显式运行。
func requireNativeFirewall(t *testing.T) {
	t.Helper()
	if os.Getenv("YZ_FIREWALL_NATIVE") != "1" {
		t.Skip("需要独立 Linux 防火墙测试环境")
	}
	marker, err := os.ReadFile("/run/yz-firewall-test")
	if err != nil || string(marker) != "isolated-rootfs\n" || os.Geteuid() != 0 {
		t.Fatal("拒绝在缺少隔离标记的环境中修改防火墙")
	}
	if _, err := net.InterfaceByName("yzfw0"); err != nil {
		t.Fatal("缺少专用测试网络接口")
	}
}

type nativeProbe struct {
	Protocol string
	Address  string
	Port     int
	Open     bool
}

type nativeCommands struct{ t *testing.T }

func (nativeCommands) Available(name string) bool { return (systemCommands{}).Available(name) }

func (c nativeCommands) Run(ctx context.Context, name, input string, args ...string) (string, error) {
	output, err := (systemCommands{}).Run(ctx, name, input, args...)
	if err != nil && !(name == "firewall-cmd" && firewalldInactive(output)) {
		c.t.Logf("原生命令 %s %v：%s", name, args, strings.TrimSpace(output))
	}
	return output, err
}

func TestNativeClientProbe(t *testing.T) {
	raw := os.Getenv("YZ_FIREWALL_PROBES")
	if raw == "" {
		t.Skip("由原生测试在客户端命名空间中调用")
	}
	var probes []nativeProbe
	if err := json.Unmarshal([]byte(raw), &probes); err != nil {
		t.Fatal(err)
	}
	for _, probe := range probes {
		address := net.JoinHostPort(probe.Address, fmt.Sprint(probe.Port))
		conn, err := net.DialTimeout(probe.Protocol, address, 750*time.Millisecond)
		open := false
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(750 * time.Millisecond))
			payload := []byte("firewall-native-echo")
			if _, err = conn.Write(payload); err == nil {
				buffer := make([]byte, len(payload))
				_, err = io.ReadFull(conn, buffer)
				open = err == nil && bytes.Equal(buffer, payload)
			}
			conn.Close()
		}
		if open != probe.Open {
			t.Errorf("%s %s 可达=%v，预期=%v，错误=%v", probe.Protocol, address, open, probe.Open, err)
		}
	}
}

func probeNative(t *testing.T, probes ...nativeProbe) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(probes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", "yzfw-client", exe, "-test.run=^TestNativeClientProbe$")
	cmd.Env = append(os.Environ(), "YZ_FIREWALL_PROBES="+string(data))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("客户端探测失败：%v\n%s", err, output)
	}
}

func nativeCommand(t *testing.T, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := (systemCommands{}).Run(ctx, name, "", args...)
	if err != nil {
		t.Fatalf("测试环境命令失败 %s：%v\n%s", name, err, output)
	}
	return output
}

func nativeEcho(t *testing.T, port int) {
	t.Helper()
	tcp, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tcp.Close() })
	go func() {
		for {
			conn, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	udp, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { udp.Close() })
	go func() {
		buffer := make([]byte, 512)
		for {
			n, peer, err := udp.ReadFrom(buffer)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(buffer[:n], peer)
		}
	}()
}

func TestNativeFirewallLifecycle(t *testing.T) {
	requireNativeFirewall(t)
	backend, redirect := os.Getenv("YZ_FIREWALL_BACKEND"), os.Getenv("YZ_FIREWALL_REDIRECT")
	if (backend != "ufw" && backend != "firewalld") || (redirect != "nftables" && redirect != "iptables") {
		t.Fatal("必须指定测试后端")
	}
	t.Logf("验证 %s + %s", backend, redirect)
	if redirect == "iptables" {
		for _, name := range []string{"iptables", "ip6tables"} {
			nativeCommand(t, name, "-t", "nat", "-N", "YZ_NATIVE_MANUAL")
			nativeCommand(t, name, "-t", "nat", "-A", "YZ_NATIVE_MANUAL", "-m", "comment", "--comment", "manual-fixture", "-j", "RETURN")
		}
	}
	ctx := context.Background()
	cfg := config.FirewallConfig{Backend: "auto", RedirectBackend: redirect, StateDir: t.TempDir()}
	b, err := newSystemBackend(cfg, "f1e2d3c4b5a6", nativeCommands{t})
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(b)
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("清理失败：%v", err)
		}
	})
	nativeEcho(t, 28443)
	nativeEcho(t, 28444)
	probe := func(protocol string, port int, open bool) {
		t.Helper()
		probeNative(t, nativeProbe{protocol, "192.0.2.1", port, open}, nativeProbe{protocol, "2001:db8:1::1", port, open})
	}
	apply := func(owner string, node *model.NodeSpec) {
		t.Helper()
		if err := m.Apply(ctx, owner, node, "xray"); err != nil {
			t.Fatal(err)
		}
	}
	release := func(owner string) {
		t.Helper()
		if err := m.Release(ctx, owner); err != nil {
			t.Fatal(err)
		}
	}
	probe("tcp", 28443, false)
	probe("udp", 28443, false)

	// 手工规则先于 Node 创建，停止后仍应完整保留。
	if backend == "ufw" {
		nativeCommand(t, "ufw", "allow", "log", "28444/tcp", "comment", "manual-fixture")
	} else {
		nativeCommand(t, "firewall-cmd", "--add-port=28444/tcp")
	}
	manual := &model.NodeSpec{Protocol: "vless", ServerPort: 28444}
	apply("manual", manual)
	release("manual")
	probe("tcp", 28444, true)
	if backend == "ufw" && !strings.Contains(nativeCommand(t, "ufw", "status", "numbered"), "(log) # manual-fixture") {
		t.Fatal("手工放行规则的日志或注释被改写")
	}

	tcp := &model.NodeSpec{Protocol: "vless", ServerPort: 28443}
	apply("tcp-first", tcp)
	apply("tcp-first", tcp)
	apply("tcp-second", tcp)
	probe("tcp", 28443, true)
	release("tcp-first")
	probe("tcp", 28443, true)
	release("tcp-second")
	probe("tcp", 28443, false)

	hy2 := &model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 28443, PortHopping: "29440-29442,29500"}
	apply("hy2", hy2)
	apply("hy2", hy2)
	for _, port := range []int{28443, 29440, 29441, 29442, 29500} {
		probe("udp", port, true)
	}
	probe("udp", 29443, false)
	t.Log("双栈单端口、连续范围、离散端口和共享引用通过")

	// 外部删除规则或 firewalld 重载后，按运行节点恢复。
	if backend == "ufw" {
		for _, owned := range append([]ownedRule(nil), b.state.Owned...) {
			if owned.Rule.Ports.From == 28443 {
				nativeCommand(t, "ufw", b.ufwArgs(owned.Rule, true)...)
			}
		}
	} else {
		if redirect == "iptables" {
			for _, name := range []string{"iptables", "ip6tables"} {
				if !strings.Contains(nativeCommand(t, name, "-t", "nat", "-S"), "manual-fixture") {
					t.Fatal("Node 操作期间修改了其他 NAT 链")
				}
			}
		}
		nativeCommand(t, "firewall-cmd", "--reload")
		if redirect == "iptables" {
			// firewalld 自身的重载会清空 xtables 运行时对象，此后重建手工对照规则。
			for _, name := range []string{"iptables", "ip6tables"} {
				if !strings.Contains(nativeCommand(t, name, "-t", "nat", "-S"), "-N YZ_NATIVE_MANUAL") {
					nativeCommand(t, name, "-t", "nat", "-N", "YZ_NATIVE_MANUAL")
					nativeCommand(t, name, "-t", "nat", "-A", "YZ_NATIVE_MANUAL", "-m", "comment", "--comment", "manual-fixture", "-j", "RETURN")
				}
			}
		}
	}
	if err := m.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	probe("udp", 29440, true)
	hy2.PortHopping = "29600-29602,29700"
	apply("hy2", hy2)
	for _, port := range []int{29440, 29442, 29500} {
		probe("udp", port, false)
	}
	for _, port := range []int{29600, 29602, 29700} {
		probe("udp", port, true)
	}

	// 指定地址走 DNAT；另一地址族不能继续使用旧转发。
	hy2.ListenIP = "192.0.2.1"
	apply("hy2", hy2)
	probeNative(t, nativeProbe{"udp", "192.0.2.1", 29600, true}, nativeProbe{"udp", "2001:db8:1::1", 29600, false})
	hy2.ListenIP = "2001:db8:1::1"
	apply("hy2", hy2)
	probeNative(t, nativeProbe{"udp", "192.0.2.1", 29600, false}, nativeProbe{"udp", "2001:db8:1::1", 29600, true})
	release("hy2")
	release("hy2")
	probe("udp", 28443, false)
	probe("udp", 29600, false)
	if len(b.state.Owned) != 0 || b.state.Redirect != "" {
		t.Fatal("停止后仍有托管规则")
	}
	t.Log("规则恢复、范围重载、指定地址和重复停止通过")

	if backend == "ufw" {
		nativeCommand(t, "ufw", "deny", "28446/tcp", "comment", "manual-deny")
		blocked := &model.NodeSpec{Protocol: "vless", ServerPort: 28446}
		if err := m.Apply(ctx, "blocked", blocked, "xray"); err == nil {
			t.Fatal("应报告已有拒绝规则的冲突")
		}
		release("blocked")
		if !strings.Contains(nativeCommand(t, "ufw", "status", "numbered"), "manual-deny") {
			t.Fatal("手工拒绝规则丢失")
		}
		apply("tcp", tcp)
		nativeCommand(t, "ufw", "disable")
		release("tcp")
		if strings.Contains(nativeCommand(t, "ufw", "show", "added"), b.nftOwner()) {
			t.Fatal("UFW 停用后仍残留托管持久规则")
		}
		nativeCommand(t, "ufw", "--force", "enable")
	}

	// 模拟进程异常退出：仅释放文件锁，新进程从持久状态回收规则。
	hy2.ListenIP = ""
	apply("hy2", hy2)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	m.closed = true
	recovered, err := newSystemBackend(cfg, b.scope, nativeCommands{t})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if err := recovered.Apply(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	probe("udp", 29600, false)
	if len(recovered.state.Owned) != 0 || recovered.state.Redirect != "" {
		t.Fatal("恢复时未回收上次进程的规则")
	}
	if redirect == "iptables" {
		for _, name := range []string{"iptables", "ip6tables"} {
			if !strings.Contains(nativeCommand(t, name, "-t", "nat", "-S", "YZ_NATIVE_MANUAL"), "manual-fixture") {
				t.Fatal("其他 NAT 链被修改")
			}
		}
	}
	t.Log("异常退出恢复和归属清理通过")
}
