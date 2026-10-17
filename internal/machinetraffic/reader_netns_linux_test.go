//go:build linux

package machinetraffic

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// 用独立的网络和挂载空间模拟容器，所有网卡随子进程退出释放。
func TestReadContainerNetworkNamespace(t *testing.T) {
	const childEnv = "YZ_TEST_TRAFFIC_NETNS_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("隔离网卡测试需要 root 和 unshare")
		}
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("未安装 unshare")
		}
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(unshare, "--net", "--mount", "--fork", binary, "-test.run=^TestReadContainerNetworkNamespace$", "-test.v", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			if strings.HasPrefix(string(output), "unshare:") && strings.Contains(string(output), "Operation not permitted") {
				t.Skipf("环境不允许隔离网络空间: %s", output)
			}
			t.Fatalf("isolated container network: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	links, err := netlink.LinkList()
	if err != nil || len(links) != 1 || links[0].Attrs().Name != "lo" {
		t.Fatalf("test requires a fresh network namespace: %v", err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("sysfs", "/sys", "sysfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		t.Fatal(err)
	}
	// 对端网络空间不写入 /run/netns，也不修改宿主机的网卡和路由。
	type namespaceResult struct {
		ns  netns.NsHandle
		err error
	}
	created := make(chan namespaceResult)
	go func() {
		runtime.LockOSThread()
		ns, err := netns.New()
		created <- namespaceResult{ns, err}
		// 不解锁已切换网络空间的线程，退出时由 Go 销毁。
	}()
	result := <-created
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.ns.Close()
	peer, err := netlink.NewHandleAt(result.ns)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	for _, cfg := range []struct{ name, local, remote, gateway string }{
		{"eth0", "2001:db8:1::2/64", "2001:db8:1::1/64", "2001:db8:1::1"},
		{"eth1", "192.0.2.2/24", "192.0.2.1/24", "192.0.2.1"},
	} {
		attrs := netlink.NewLinkAttrs()
		attrs.Name = cfg.name
		if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: attrs, PeerName: "peer-" + cfg.name, PeerNamespace: netlink.NsFd(result.ns)}); err != nil {
			t.Fatal(err)
		}
		localLink, err := netlink.LinkByName(cfg.name)
		if err != nil {
			t.Fatal(err)
		}
		peerLink, err := peer.LinkByName("peer-" + cfg.name)
		if err != nil {
			t.Fatal(err)
		}
		localAddr, err := netlink.ParseAddr(cfg.local)
		if err != nil {
			t.Fatal(err)
		}
		remoteAddr, err := netlink.ParseAddr(cfg.remote)
		if err != nil {
			t.Fatal(err)
		}
		localAddr.Flags, remoteAddr.Flags = unix.IFA_F_NODAD, unix.IFA_F_NODAD
		for _, err := range []error{netlink.AddrAdd(localLink, localAddr), peer.AddrAdd(peerLink, remoteAddr), netlink.LinkSetUp(localLink), peer.LinkSetUp(peerLink)} {
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: localLink.Attrs().Index, Gw: net.ParseIP(cfg.gateway)}); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := Read(nil)
	if err != nil || len(initial.Counters) != 2 || initial.Boot == "" {
		t.Fatalf("auto-select container uplinks: %+v, %v", initial, err)
	}
	for _, cfg := range []struct{ name, network, address string }{
		{"eth0", "udp6", "[2001:db8:1::1]:0"},
		{"eth1", "udp4", "192.0.2.1:0"},
	} {
		// 在对端建立套接字后切回；套接字仍属于创建时的网络空间。
		var server net.PacketConn
		func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			current, err := netns.Get()
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if err := netns.Set(result.ns); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := netns.Set(current); err != nil {
					panic(fmt.Sprintf("restore test namespace: %v", err))
				}
			}()
			server, err = net.ListenPacket(cfg.network, cfg.address)
			if err != nil {
				t.Fatal(err)
			}
		}()
		defer server.Close()
		client, err := net.Dial(cfg.network, server.LocalAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		deadline := time.Now().Add(5 * time.Second)
		if err := server.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		if err := client.SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		payload := bytes.Repeat([]byte("meter"), 100)
		if _, err := client.Write(payload); err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1024)
		n, from, err := server.ReadFrom(buffer)
		if err != nil || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("container uplink send: %v", err)
		}
		if _, err := server.WriteTo(buffer[:n], from); err != nil {
			t.Fatal(err)
		}
		n, err = client.Read(buffer)
		if err != nil || !bytes.Equal(buffer[:n], payload) {
			t.Fatalf("container uplink receive: %v", err)
		}
		reading, err := Read([]string{cfg.name, cfg.name})
		if err != nil || len(reading.Counters) != 1 {
			t.Fatalf("explicit uplink deduplication: %+v, %v", reading, err)
		}
		before, after := initial.Counters[cfg.name], reading.Counters[cfg.name]
		if after.RX < before.RX+uint64(len(payload)) || after.TX < before.TX+uint64(len(payload)) {
			t.Fatalf("%s counters did not include traffic: before=%+v after=%+v", cfg.name, before, after)
		}
	}
	names, err := billingInterfaces("/sys/class/net", []string{"eth1", "eth0", "eth1"}, netlink.LinkByName)
	if err != nil || !reflect.DeepEqual(names, []string{"eth0", "eth1"}) {
		t.Fatalf("dual uplink deduplication: %v, %v", names, err)
	}
	if _, err := Read([]string{"lo"}); err == nil {
		t.Fatal("loopback accepted for billing")
	}
}
