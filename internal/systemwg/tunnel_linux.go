//go:build linux

package systemwg

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// tunnel 不创建持久命名空间；关闭设备和描述符后由内核回收网络空间。
type tunnel struct {
	ns         netns.NsHandle
	wg         *device.Device
	routes     []*net.IPNet
	listenPort int
	once       sync.Once
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
	return nil
}
func (t *tunnel) close() {
	t.once.Do(func() { t.wg.Close(); _ = t.ns.Close() })
}

func newTunnel(w *model.RelayWireGuardConfig, endpoint string, port int) (*tunnel, error) {
	private, err := decodeKey(w.PrivateKey)
	if err != nil {
		return nil, err
	}
	public, err := decodeKey(w.PeerPublicKey)
	if err != nil {
		return nil, err
	}
	if w.MTU < 1280 || w.MTU > 65535 || port < 0 || port > 65535 || w.Keepalive < 0 || w.Keepalive > 65535 {
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
	for _, value := range w.AllowedIPs {
		_, subnet, err := net.ParseCIDR(value)
		if err != nil {
			return nil, errors.New("系统 WG 允许网段无效")
		}
		allowed = append(allowed, subnet)
	}
	if len(addresses) == 0 || len(allowed) == 0 {
		return nil, errors.New("系统 WG 缺少地址或允许网段")
	}
	if endpoint != "" {
		a, err := net.ResolveUDPAddr("udp", endpoint)
		if err != nil {
			return nil, errors.New("无法解析系统 WG 对端地址")
		}
		endpoint = a.String()
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
		if err := os.WriteFile("/proc/sys/net/ipv4/tcp_congestion_control", []byte("bbr"), 0); err != nil {
			fail(fmt.Errorf("系统 WG 需要 Linux BBR 支持: %w", err))
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
	wg := device.NewDevice(r.dev, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t := &tunnel{ns: r.ns, wg: wg, routes: allowed, listenPort: port}
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\npublic_key=%s\nreplace_allowed_ips=true\n", private, public)
	for _, subnet := range allowed {
		fmt.Fprintf(&b, "allowed_ip=%s\n", subnet)
	}
	if endpoint != "" {
		fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	}
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", w.Keepalive)
	if err := wg.IpcSet(b.String()); err != nil {
		t.close()
		return nil, errors.New("配置系统 WG 设备失败")
	}
	return t, nil
}

func decodeKey(value string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 {
		return "", errors.New("系统 WG 密钥格式无效")
	}
	return hex.EncodeToString(b), nil
}
