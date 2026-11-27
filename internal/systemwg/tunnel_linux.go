//go:build linux

package systemwg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/wireguard-go/conn"
	"github.com/sagernet/wireguard-go/device"
	"github.com/sagernet/wireguard-go/tun"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// tunnel 不创建持久命名空间；关闭设备和描述符后由内核回收网络空间。
type tunnel struct {
	ns              netns.NsHandle
	wg              *device.Device
	routes          []*net.IPNet
	listenPort      int
	once            sync.Once
	endpointHost    string
	endpointPort    int
	endpointAddress string
	endpointKeys    []string
	cancel          context.CancelFunc
	done            chan struct{}
}

func (t *tunnel) path() string { return fmt.Sprintf("/proc/self/fd/%d", t.ns) }
func (t *tunnel) start() error {
	// 等旧核心释放监听后才绑定固定端口，构造阶段不能抢占正在使用的落地端口。
	// 入口使用已分配的随机端口；重复写入 0 会换端口，令提前发出的握手应答丢失。
	if t.listenPort != 0 {
		if err := t.wg.IpcSet(fmt.Sprintf("listen_port=%d\n", t.listenPort)); err != nil {
			return errors.New("绑定系统 WG 监听端口失败")
		}
	}
	h, err := netlink.NewHandleAt(t.ns)
	if err != nil {
		return err
	}
	defer h.Close()
	link, err := h.LinkByName("yz-wg0")
	if err != nil {
		return err
	}
	if err := t.wg.Up(); err != nil {
		return err
	}
	if err := h.LinkSetUp(link); err != nil {
		return err
	}
	for _, subnet := range t.routes {
		if err := h.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: subnet}); err != nil {
			return fmt.Errorf("安装系统 WG 路由失败: %w", err)
		}
	}
	if t.endpointHost != "" && t.cancel == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t.cancel, t.done = cancel, make(chan struct{})
		go t.refreshEndpoint(ctx)
	}
	return nil
}
func (t *tunnel) close() {
	t.once.Do(func() {
		if t.cancel != nil {
			t.cancel()
			<-t.done
		}
		t.wg.Close()
		_ = t.ns.Close()
	})
}

func newTunnel(w *model.RelayWireGuardConfig, endpoint string, port int) (*tunnel, error) {
	private, err := decodeKey(w.PrivateKey)
	if err != nil {
		return nil, err
	}
	peers := w.Peers
	if len(peers) == 0 {
		peers = []model.RelayWireGuardPeer{{PublicKey: w.PeerPublicKey, AllowedIPs: w.AllowedIPs}}
	}
	for _, peer := range peers {
		if _, err := decodeKey(peer.PublicKey); err != nil {
			return nil, err
		}
	}
	if w.MTU < 1280 || w.MTU > 1500 || port < 0 || port > 65535 || w.Keepalive < 0 || w.Keepalive > 65535 {
		return nil, errors.New("系统 WG 的 MTU 或监听端口无效")
	}
	var addresses []*netlink.Addr
	for _, value := range w.Address {
		a, err := netlink.ParseAddr(value)
		if err != nil {
			return nil, errors.New("系统 WG 地址无效")
		}
		addresses = append(addresses, a)
	}
	var allowed []*net.IPNet
	seen := make(map[string]bool)
	for i := range peers {
		for _, value := range peers[i].AllowedIPs {
			_, subnet, err := net.ParseCIDR(value)
			if err != nil {
				return nil, errors.New("系统 WG 允许网段无效")
			}
			if key := subnet.String(); !seen[key] {
				seen[key] = true
				allowed = append(allowed, subnet)
			}
		}
	}
	if len(addresses) == 0 || len(allowed) == 0 {
		return nil, errors.New("系统 WG 缺少地址或允许网段")
	}
	var endpointHost string
	var endpointPort int
	if endpoint != "" {
		host, portText, err := net.SplitHostPort(endpoint)
		if err != nil {
			return nil, errors.New("系统 WG 对端地址无效")
		}
		endpointPort, err = strconv.Atoi(portText)
		if err != nil || endpointPort < 1 || endpointPort > 65535 {
			return nil, errors.New("系统 WG 对端端口无效")
		}
		a, err := resolveEndpoint(context.Background(), host, endpointPort)
		if err != nil {
			return nil, errors.New("无法解析系统 WG 对端地址")
		}
		endpoint = a
		if net.ParseIP(host) == nil {
			endpointHost = host
		}
	}
	type result struct {
		ns  netns.NsHandle
		dev tun.Device
		err error
	}
	ch := make(chan result, 1)
	go func() {
		// 不解锁线程：退出时 Go 销毁此线程，禁止其他任务继承该网络空间。
		runtime.LockOSThread()
		ns, err := netns.New()
		if err != nil {
			ch <- result{err: fmt.Errorf("创建系统 WG 网络空间失败: %w", err)}
			return
		}
		var dev tun.Device
		fail := func(err error) {
			if dev != nil {
				_ = dev.Close()
			}
			_ = ns.Close()
			ch <- result{err: err}
		}
		if err := configureCongestionControl(); err != nil {
			fail(err)
			return
		}
		// TCP can recover from a black-holed path MTU. This does not make oversized
		// UDP safe or change the administrator's configured interface MTU.
		if err := os.WriteFile("/proc/sys/net/ipv4/tcp_mtu_probing", []byte("1"), 0); err != nil {
			fail(fmt.Errorf("配置系统 WG TCP MTU 探测失败: %w", err))
			return
		}
		dev, err = tun.CreateTUN("yz-wg0", w.MTU)
		if err != nil {
			fail(fmt.Errorf("创建系统 WG 网卡失败: %w", err))
			return
		}
		h, err := netlink.NewHandle()
		if err != nil {
			fail(err)
			return
		}
		defer h.Close()
		link, err := h.LinkByName("yz-wg0")
		if err != nil {
			fail(err)
			return
		}
		for _, a := range addresses {
			if a.IP.To4() == nil {
				a.Flags |= unix.IFA_F_NODAD
			}
			if err := h.AddrAdd(link, a); err != nil {
				fail(err)
				return
			}
		}
		lo, err := h.LinkByName("lo")
		if err != nil {
			fail(err)
			return
		}
		if err := h.LinkSetUp(lo); err != nil {
			fail(err)
			return
		}
		ch <- result{ns: ns, dev: dev}
	}()
	r := <-ch
	if r.err != nil {
		return nil, r.err
	}
	// 加密 UDP 套接字在宿主网络空间创建，不受隧道内的路由影响。
	// Bound per-link workers instead of multiplying the host CPU count by every
	// landing. The already pinned implementation shares the WireGuard wire format.
	wg := device.NewDevice(context.Background(), r.dev, conn.NewDefaultBind(nil),
		device.NewLogger(device.LogLevelSilent, ""), min(2, runtime.GOMAXPROCS(0)))
	t := &tunnel{ns: r.ns, wg: wg, routes: allowed, listenPort: port,
		endpointHost: endpointHost, endpointPort: endpointPort, endpointAddress: endpoint}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\n", private)
	for _, peer := range peers {
		public, _ := decodeKey(peer.PublicKey)
		t.endpointKeys = append(t.endpointKeys, public)
		fmt.Fprintf(&b, "public_key=%s\nreplace_allowed_ips=true\n", public)
		for _, subnet := range peer.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", subnet)
		}
		if endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
		}
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", w.Keepalive)
	}
	if err := wg.IpcSet(b.String()); err != nil {
		t.close()
		return nil, errors.New("配置系统 WG 设备失败")
	}
	return t, nil
}

func resolveEndpoint(ctx context.Context, host string, port int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", err
	}
	if len(addresses) == 0 {
		return "", errors.New("WG 对端无可用地址")
	}
	for _, address := range addresses {
		target := net.JoinHostPort(address.Unmap().String(), strconv.Itoa(port))
		// UDP connect only checks local routing; it sends no probe or WG packet.
		// A host without IPv6 routing can still use the domain's IPv4 endpoint.
		probe, err := (&net.Dialer{}).DialContext(ctx, "udp", target)
		if err == nil {
			_ = probe.Close()
			return target, nil
		}
	}
	return "", errors.New("WG 对端没有可达的本机路由")
}

func (t *tunnel) refreshEndpoint(ctx context.Context) {
	defer close(t.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.updateEndpoint(ctx, resolveEndpoint)
		}
	}
}

func (t *tunnel) updateEndpoint(ctx context.Context, resolve func(context.Context, string, int) (string, error)) {
	address, err := resolve(ctx, t.endpointHost, t.endpointPort)
	if err != nil || address == t.endpointAddress || ctx.Err() != nil {
		return
	}
	var update strings.Builder
	for _, key := range t.endpointKeys {
		fmt.Fprintf(&update, "public_key=%s\nupdate_only=true\nendpoint=%s\n", key, address)
	}
	if t.wg.IpcSet(update.String()) == nil {
		t.endpointAddress = address
	}
}

func configureCongestionControl() error {
	// This runs inside the new namespace. BBR is optional; CUBIC provides a
	// usable TCP path on hosts without BBR without changing the host setting.
	for _, algorithm := range []string{"bbr", "cubic"} {
		if err := os.WriteFile("/proc/sys/net/ipv4/tcp_congestion_control", []byte(algorithm), 0); err == nil {
			return nil
		}
	}
	return errors.New("无法为系统 WG 配置 TCP 拥塞控制")
}

func decodeKey(value string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 {
		return "", errors.New("系统 WG 密钥格式无效")
	}
	return hex.EncodeToString(b), nil
}
