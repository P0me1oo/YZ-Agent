package directwg

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/sagernet/wireguard-go/conn"
	"github.com/sagernet/wireguard-go/device"
	"go4.org/netipx"
)

// 入口只使用自身 UDP 套接字，不改宿主网络接口、路由或防火墙。
type udpBind struct {
	mu         sync.Mutex
	socket     *net.UDPConn
	runtime    *Runtime
	blockedSet *netipx.IPSet
}
type endpoint struct {
	address  netip.AddrPort
	bind     *udpBind
	header   [16]byte
	size     int
	admitted atomic.Bool
}

func (e *endpoint) ClearSrc()           {}
func (e *endpoint) SrcToString() string { return "" }
func (e *endpoint) DstToString() string { return e.address.String() }
func (e *endpoint) DstIP() netip.Addr   { return e.address.Addr() }
func (e *endpoint) SrcIP() netip.Addr   { return netip.Addr{} }
func (e *endpoint) DstToBytes() []byte {
	v := e.address.Addr().As16()
	b := append([]byte{}, v[:]...)
	return binary.BigEndian.AppendUint16(b, e.address.Port())
}
func (e *endpoint) FromPeer(key [32]byte) {
	e.admitted.Store(e.bind.runtime.authenticated(e.header, e.size, base64.StdEncoding.EncodeToString(key[:]), e.address.Addr()))
}
func (b *udpBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.socket != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	ip := b.runtime.config.ListenIP
	if ip == "" {
		ip = "::"
	}
	address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(ip, fmt.Sprint(port)))
	if err != nil {
		return nil, 0, err
	}
	socket, err := net.ListenUDP("udp", address)
	if err != nil {
		return nil, 0, err
	}
	b.socket = socket
	receive := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		for {
			n, source, err := socket.ReadFromUDPAddrPort(packets[0])
			if err != nil {
				return 0, err
			}
			source = netip.AddrPortFrom(source.Addr().Unmap(), source.Port())
			if b.blocked(source.Addr()) {
				continue
			}
			e := &endpoint{address: source, bind: b, size: n}
			if n >= 16 {
				copy(e.header[:], packets[0][:16])
			}
			sizes[0], eps[0] = n, e
			return 1, nil
		}
	}
	return []conn.ReceiveFunc{receive}, uint16(socket.LocalAddr().(*net.UDPAddr).Port), nil
}
func (b *udpBind) blocked(a netip.Addr) bool {
	return b.blockedSet != nil && b.blockedSet.Contains(a)
}
func (b *udpBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.socket == nil {
		return nil
	}
	err := b.socket.Close()
	b.socket = nil
	return err
}
func (b *udpBind) SetMark(mark uint32) error {
	if mark != 0 {
		return fmt.Errorf("普通 WG 入口不支持套接字标记")
	}
	return nil
}
func (b *udpBind) BatchSize() int                                 { return 1 }
func (b *udpBind) SetReservedForEndpoint(netip.AddrPort, [3]byte) {}
func (b *udpBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	a, err := netip.ParseAddrPort(s)
	return &endpoint{address: a, bind: b}, err
}
func (b *udpBind) Send(bufs [][]byte, ep conn.Endpoint, offset int) error {
	e, ok := ep.(*endpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	b.mu.Lock()
	socket := b.socket
	b.mu.Unlock()
	if socket == nil {
		return net.ErrClosed
	}
	for _, p := range bufs {
		p = p[offset:]
		if len(p) >= 4 && binary.LittleEndian.Uint32(p[:4]) == device.MessageTransportType && !e.admitted.Load() {
			continue
		}
		if b.blocked(e.address.Addr()) {
			continue
		}
		if _, err := socket.WriteToUDPAddrPort(p, e.address); err != nil {
			return err
		}
	}
	return nil
}
