package directwg

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	wg "github.com/sagernet/sing-box/transport/wireguard"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/wireguard-go/device"
	"go4.org/netipx"
)

type Router interface {
	WireGuardTCP(context.Context, net.Conn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc)
	WireGuardUDP(context.Context, N.PacketConn, M.Socksaddr, M.Socksaddr, N.CloseHandlerFunc)
}

type authenticatedPacket struct {
	id    int
	at    time.Time
	owner *userState
}

type Runtime struct {
	mu        sync.Mutex
	state     *State
	router    Router
	config    *model.NodeSpec
	dev       *device.Device
	stack     wg.Device
	bind      *udpBind
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	packetsMu sync.Mutex
	packets   map[[16]byte]authenticatedPacket
}

func start(n *model.NodeSpec, users []model.UserSpec, state *State, router Router) (*Runtime, error) {
	if err := model.ValidateDirectWireGuard(n); err != nil {
		return nil, err
	}
	if err := validateUsers(users, n.WireGuard.Address); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{state: state, router: router, config: n, ctx: ctx, cancel: cancel, done: make(chan struct{}), packets: make(map[[16]byte]authenticatedPacket)}
	addresses := make([]netip.Prefix, 0, len(n.WireGuard.Address))
	for _, v := range n.WireGuard.Address {
		addresses = append(addresses, netip.MustParsePrefix(v))
	}
	stack, err := wg.NewDevice(wg.DeviceOptions{Context: ctx, Logger: logger.NOP(), Handler: r, MTU: uint32(n.WireGuard.MTU), Address: addresses, UDPTimeout: 90 * time.Second})
	if err != nil {
		cancel()
		return nil, err
	}
	r.stack = stack
	r.bind = &udpBind{runtime: r}
	var blocked netipx.IPSetBuilder
	for _, v := range n.SourceBlockCIDRs {
		p, e := netip.ParsePrefix(v)
		if e != nil {
			cancel()
			stack.Close()
			return nil, fmt.Errorf("大陆来源网段无效")
		}
		blocked.AddPrefix(p)
	}
	r.bind.blockedSet, err = blocked.IPSet()
	if err != nil {
		cancel()
		stack.Close()
		return nil, err
	}
	if err = stack.Start(); err != nil {
		cancel()
		stack.Close()
		return nil, err
	}
	r.dev = device.NewDevice(ctx, &trackedDevice{Device: stack, runtime: r}, r.bind, &device.Logger{Verbosef: func(string, ...any) {}, Errorf: func(string, ...any) {}}, 0)
	stack.SetDevice(r.dev)
	key, _ := base64.StdEncoding.DecodeString(n.WireGuard.PrivateKey)
	if err = r.dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\n", hex.EncodeToString(key), n.ServerPort)); err == nil {
		err = r.updateUsers(users)
	}
	if err == nil {
		err = r.dev.Up()
	}
	if err != nil {
		cancel()
		r.dev.Close()
		state.replaceUsers(nil)
		return nil, fmt.Errorf("启动普通 WireGuard 入口失败: %w", err)
	}
	go r.reap()
	return r, nil
}

func (r *Runtime) updateUsers(users []model.UserSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.updateUsersLocked(users)
}

func (r *Runtime) updateUsersLocked(users []model.UserSpec) error {
	if r.ctx.Err() != nil {
		return net.ErrClosed
	}
	if err := validateUsers(users, r.config.WireGuard.Address); err != nil {
		return err
	}
	next := make(map[string]bool)
	for _, u := range users {
		next[u.WireGuard.PublicKey] = true
	}
	var ipc strings.Builder
	r.state.mu.Lock()
	for key := range r.state.byKey {
		if !next[key] {
			raw, _ := base64.StdEncoding.DecodeString(key)
			fmt.Fprintf(&ipc, "public_key=%s\nremove=true\n", hex.EncodeToString(raw))
		}
	}
	r.state.mu.Unlock()
	for _, u := range users {
		raw, _ := base64.StdEncoding.DecodeString(u.WireGuard.PublicKey)
		fmt.Fprintf(&ipc, "public_key=%s\nreplace_allowed_ips=true\n", hex.EncodeToString(raw))
		for _, a := range u.WireGuard.Address {
			fmt.Fprintf(&ipc, "allowed_ip=%s\n", a)
		}
	}
	// 先撤销旧用户的授权和连接，避免密钥切换期间继续转发旧身份。
	r.state.replaceUsers(users)
	if err := r.dev.IpcSet(ipc.String()); err != nil {
		r.cancel()
		r.state.replaceUsers(nil)
		return fmt.Errorf("更新 WireGuard 用户失败")
	}
	return nil
}

func (r *Runtime) Close() {
	r.cancel()
	r.state.replaceUsers(nil)
	r.mu.Lock()
	r.dev.Close()
	r.mu.Unlock()
	<-r.done
}

func (r *Runtime) reap() {
	defer close(r.done)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case now := <-timer.C:
			r.mu.Lock()
			r.state.mu.Lock()
			var users []model.UserSpec
			expired := false
			for _, u := range r.state.users {
				if validAt(u, now) {
					users = append(users, u.spec)
				} else {
					expired = true
				}
			}
			r.state.mu.Unlock()
			if expired {
				_ = r.updateUsersLocked(users)
			}
			r.mu.Unlock()
			r.packetsMu.Lock()
			for k, v := range r.packets {
				if now.Sub(v.at) > 30*time.Second {
					delete(r.packets, k)
				}
			}
			r.packetsMu.Unlock()
		}
	}
}

func (r *Runtime) JudgeFlow(network uint8, source, destination netip.AddrPort, _ []byte) tun.FlowVerdict {
	if network != 6 && network != 17 {
		return tun.FlowVerdict{Action: tun.ActionReject}
	}
	if id, _ := r.state.userForAddress(source.Addr()); id == 0 {
		return tun.FlowVerdict{Action: tun.ActionReject}
	}
	return tun.FlowVerdict{Action: tun.ActionAccept}
}
func (r *Runtime) NewDNSPacket([]byte, M.Socksaddr, M.Socksaddr, N.PacketWriter) {}

func (r *Runtime) NewConnectionEx(_ context.Context, c net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	id, public := r.state.userForAddress(source.Addr)
	owner, release, ok := r.state.track(id, c, public)
	if !ok {
		c.Close()
		if onClose != nil {
			onClose(net.ErrClosed)
		}
		return
	}
	ctx, cancel := context.WithCancel(owner.ctx)
	limited := &limitedConn{Conn: c, state: r.state, owner: owner, id: id, ctx: ctx, cancel: cancel}
	var once sync.Once
	done := func(err error) {
		once.Do(func() {
			limited.Close()
			release()
			if onClose != nil {
				onClose(err)
			}
		})
	}
	r.router.WireGuardTCP(ctx, limited, M.Socksaddr{Addr: public, Port: source.Port}, destination, done)
}
func (r *Runtime) NewPacketConnectionEx(_ context.Context, c N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	id, public := r.state.userForAddress(source.Addr)
	owner, release, ok := r.state.track(id, c, public)
	if !ok {
		c.Close()
		if onClose != nil {
			onClose(net.ErrClosed)
		}
		return
	}
	ctx, cancel := context.WithCancel(owner.ctx)
	limited := &limitedPacketConn{PacketConn: c, state: r.state, owner: owner, id: id, ctx: ctx, cancel: cancel}
	var once sync.Once
	done := func(err error) {
		once.Do(func() {
			limited.Close()
			release()
			if onClose != nil {
				onClose(err)
			}
		})
	}
	r.router.WireGuardUDP(ctx, limited, M.Socksaddr{Addr: public, Port: source.Port}, destination, done)
}

// 固定依赖保留 offset 前的 WG 数据头。认证回调登记会话编号和计数器，
// 解密设备消费同一数据头；仅靠收到的 UDP 包不能建立此关联。
type trackedDevice struct {
	wg.Device
	runtime *Runtime
}

func (d *trackedDevice) Write(bufs [][]byte, offset int) (int, error) {
	accepted := make([][]byte, 0, len(bufs))
	for _, b := range bufs {
		if offset != 16 || len(b) <= offset {
			continue
		}
		key := [16]byte(b[:16])
		d.runtime.packetsMu.Lock()
		auth, ok := d.runtime.packets[key]
		delete(d.runtime.packets, key)
		d.runtime.packetsMu.Unlock()
		source, valid := packetSource(b[offset:])
		current, _ := d.runtime.state.userForAddress(source)
		if ok && valid && current == auth.id && d.runtime.state.transfer(auth.id, 0, 0, auth.owner) {
			accepted = append(accepted, b)
		}
	}
	if len(accepted) > 0 {
		if _, err := d.Device.Write(accepted, offset); err != nil {
			return 0, err
		}
	}
	return len(bufs), nil
}
func (d *trackedDevice) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	n, err := d.Device.Read(bufs, sizes, offset)
	for i := 0; i < n; i++ {
		p := bufs[i][offset : offset+sizes[i]]
		a, ok := packetDestination(p)
		id, _ := d.runtime.state.userForAddress(a)
		if !ok || !d.runtime.state.transfer(id, 1, 0, nil) {
			sizes[i] = 0
		}
	}
	return n, err
}
func packetDestination(p []byte) (netip.Addr, bool) {
	if len(p) >= 20 && p[0]>>4 == 4 {
		return netip.AddrFrom4([4]byte(p[16:20])), true
	}
	if len(p) >= 40 && p[0]>>4 == 6 {
		return netip.AddrFrom16([16]byte(p[24:40])), true
	}
	return netip.Addr{}, false
}

func packetSource(p []byte) (netip.Addr, bool) {
	if len(p) >= 20 && p[0]>>4 == 4 {
		return netip.AddrFrom4([4]byte(p[12:16])), true
	}
	if len(p) >= 40 && p[0]>>4 == 6 {
		return netip.AddrFrom16([16]byte(p[8:24])), true
	}
	return netip.Addr{}, false
}

func (r *Runtime) authenticated(header [16]byte, size int, key string, source netip.Addr) bool {
	if binary.LittleEndian.Uint32(header[:4]) != device.MessageTransportType {
		return false
	}
	payload := size > device.MinMessageSize
	r.state.mu.Lock()
	allowed := r.state.admitLocked(key, source, payload, time.Now())
	id := r.state.byKey[key]
	owner := r.state.users[id]
	r.state.mu.Unlock()
	if !allowed || !payload {
		return allowed
	}
	r.packetsMu.Lock()
	defer r.packetsMu.Unlock()
	if len(r.packets) >= 8192 {
		return false
	}
	r.packets[header] = authenticatedPacket{id: id, at: time.Now(), owner: owner}
	return true
}
