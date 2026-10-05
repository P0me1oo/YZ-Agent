//go:build linux

package xray

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/systemwg"
	sbuf "github.com/sagernet/sing/common/buf"
	smeta "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	ssocks "github.com/sagernet/sing/protocol/socks"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	udpPacket "github.com/xtls/xray-core/common/protocol/udp"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/proxy/socks"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/stat"
	"github.com/xtls/xray-core/transport/internet/udp"
)

type systemWGKey struct{}

// 复用核心 SOCKS 出站及其流量计数，只替换本实例 WG 线路的套接字创建位置。
func init() {
	typ := reflect.TypeOf((*socks.ClientConfig)(nil))
	original := typeCreatorRegistry[typ]
	typeCreatorRegistry[typ] = func(ctx context.Context, config interface{}) (interface{}, error) {
		client, err := original(ctx, config)
		if err != nil {
			return nil, err
		}
		runtime, _ := ctx.Value(systemWGKey{}).(*systemwg.Runtime)
		handler := session.FullHandlerFromContext(ctx)
		if runtime == nil || handler == nil || runtime.XrayOutboundNamespace(handler.Tag()) == "" {
			return client, nil
		}
		return &systemWGOutbound{Outbound: client.(proxy.Outbound), runtime: runtime, tag: handler.Tag()}, nil
	}
}

func systemWGContext(ctx context.Context, runtime *systemwg.Runtime) context.Context {
	return context.WithValue(ctx, systemWGKey{}, runtime)
}

type systemWGOutbound struct {
	proxy.Outbound
	runtime *systemwg.Runtime
	tag     string
}

func (o *systemWGOutbound) Process(ctx context.Context, link *transport.Link, dialer internet.Dialer) error {
	return o.Outbound.Process(ctx, link, &systemWGDialer{Dialer: dialer, runtime: o.runtime, tag: o.tag})
}

type systemWGDialer struct {
	internet.Dialer
	runtime *systemwg.Runtime
	tag     string
}

func (d *systemWGDialer) Dial(ctx context.Context, destination xnet.Destination) (conn stat.Connection, err error) {
	// 只连接隧道落地；目标网站地址仍通过 SOCKS 传递并由落地核心处理。
	if destination.Address == nil || !destination.Address.Family().IsIPv4() || destination.Address.String() != "10.253.0.2" {
		return nil, fmt.Errorf("WG 出站拒绝非落地地址")
	}
	// Prepare 生成无 TLS、无复用的普通 SOCKS 出站，目标是单个 IPv4 地址。
	// 固定核心的 TCP/UDP 拨号同步创建套接字；不会触发域名解析或双栈并行拨号。
	// 保留原拨号器，以继续使用 Xray 的线路流量计数。
	err = d.runtime.XrayNamespaceCall(ctx, d.tag, func() error {
		opened, dialErr := d.Dialer.Dial(ctx, destination)
		if dialErr != nil {
			// 固定核心失败时可能返回内部连接为空的计数包装，不能调用 Close。
			return dialErr
		}
		conn = opened
		return nil
	})
	if err != nil && conn != nil {
		_ = conn.Close()
		conn = nil
	}
	return
}

func attachSystemWG(ctx context.Context, instance *xrayCore.Instance, runtime *systemwg.Runtime) error {
	if runtime.XrayLandingNamespace() == "" {
		return nil
	}
	manager := instance.GetFeature(inbound.ManagerType()).(inbound.Manager)
	dispatcher := instance.GetFeature(routing.DispatcherType()).(routing.Dispatcher)
	managed, ok := dispatcher.(*LimitDispatcher)
	if !ok || managed.coreContext == nil {
		return fmt.Errorf("WG 入站缺少核心调度上下文")
	}
	ctx = managed.coreContext
	ctx, cancel := context.WithCancel(ctx)
	handler := &systemWGInbound{ctx: ctx, cancel: cancel, runtime: runtime, dispatcher: dispatcher, connections: make(map[net.Conn]struct{})}
	if err := manager.AddHandler(ctx, handler); err != nil {
		cancel()
		return err
	}
	return nil
}

// 落地直接从 WG 网卡接收 SOCKS，并交给当前 Xray 调度器，不创建第二个核心实例。
type systemWGInbound struct {
	ctx         context.Context
	cancel      context.CancelFunc
	runtime     *systemwg.Runtime
	dispatcher  routing.Dispatcher
	mu          sync.Mutex
	listener    net.Listener
	connections map[net.Conn]struct{}
	closed      bool
	wg          sync.WaitGroup
}

func (*systemWGInbound) Tag() string                            { return relayLandingInboundTag }
func (*systemWGInbound) ReceiverSettings() *serial.TypedMessage { return nil }
func (*systemWGInbound) ProxySettings() *serial.TypedMessage    { return nil }

func (h *systemWGInbound) Start() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return net.ErrClosed
	}
	if h.listener != nil {
		return nil
	}
	var listener net.Listener
	err := h.runtime.XrayNamespaceCall(h.ctx, "", func() error {
		var err error
		listener, err = (&net.ListenConfig{}).Listen(h.ctx, "tcp4", "10.253.0.2:1080")
		return err
	})
	if err != nil {
		if listener != nil {
			_ = listener.Close()
		}
		return err
	}
	h.listener = listener
	h.wg.Add(1)
	go h.accept(listener)
	return nil
}

func (h *systemWGInbound) accept(listener net.Listener) {
	defer h.wg.Done()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			_ = conn.Close()
			return
		}
		h.connections[conn] = struct{}{}
		h.wg.Add(1)
		h.mu.Unlock()
		go h.serve(conn)
	}
}

func (h *systemWGInbound) serve(conn net.Conn) {
	defer h.wg.Done()
	defer func() { _ = conn.Close(); h.mu.Lock(); delete(h.connections, conn); h.mu.Unlock() }()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	ctx = session.ContextWithInbound(ctx, &session.Inbound{Tag: relayLandingInboundTag, Source: xnet.DestinationFromAddr(conn.RemoteAddr()), Local: xnet.DestinationFromAddr(conn.LocalAddr()), Conn: conn})
	ctx = session.ContextWithContent(ctx, &session.Content{})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{}})
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// 业务读取继续复用握手缓冲，保留客户端随握手提前发送的数据。
	reader := bufio.NewReader(conn)
	buffered := &systemWGBufferedConn{Conn: conn, reader: reader}
	_ = ssocks.HandleConnectionEx(ctx, buffered, reader, nil, h, h, 5*time.Minute, smeta.SocksaddrFromNet(conn.RemoteAddr()), func(error) { _ = conn.Close() })
}

type systemWGBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *systemWGBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (h *systemWGInbound) Close() error {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		h.cancel()
		if h.listener != nil {
			_ = h.listener.Close()
		}
		for conn := range h.connections {
			_ = conn.Close()
		}
	}
	h.mu.Unlock()
	h.wg.Wait()
	return nil
}

func (h *systemWGInbound) ListenPacket(config net.ListenConfig, ctx context.Context, network, address string) (conn net.PacketConn, err error) {
	// UDP 关联建立后，控制连接不再使用握手阶段的短超时。
	if inbound := session.InboundFromContext(ctx); inbound != nil && inbound.Conn != nil {
		_ = inbound.Conn.SetReadDeadline(time.Time{})
	}
	err = h.runtime.XrayNamespaceCall(ctx, "", func() error { conn, err = config.ListenPacket(ctx, network, address); return err })
	if err != nil && conn != nil {
		_ = conn.Close()
		conn = nil
	}
	return
}

func (h *systemWGInbound) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination smeta.Socksaddr, onClose N.CloseHandlerFunc) {
	defer onClose(nil)
	_ = conn.SetReadDeadline(time.Time{})
	// 在并发读写开始前完成应答，避免延迟应答重复写入数据流。
	if lazy, ok := conn.(*ssocks.LazyConn); ok {
		if err := lazy.ConnHandshakeSuccess(lazy.Conn); err != nil {
			return
		}
		conn = lazy.Conn
	}
	err := h.dispatcher.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress(destination.AddrString()), xnet.Port(destination.Port)), &transport.Link{Reader: buf.NewReader(conn), Writer: buf.NewWriter(conn)})
	if err != nil {
		_ = conn.Close()
	}
}

func (h *systemWGInbound) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination smeta.Socksaddr, onClose N.CloseHandlerFunc) {
	defer onClose(nil)
	defer conn.Close()
	// UDP 保持逐包目标地址；回包直接写回隧道，不转成 TCP。
	dispatcher := udp.NewDispatcher(h.dispatcher, func(_ context.Context, packet *udpPacket.Packet) {
		defer packet.Payload.Release()
		destination := smeta.ParseSocksaddr(packet.Source.NetAddr())
		// SOCKS 写入会向前追加包头，不能直接包装没有预留空间的 Xray 缓冲。
		headroom := 3 + smeta.SocksaddrSerializer.AddrPortLen(destination)
		response := sbuf.NewSize(headroom + int(packet.Payload.Len()))
		response.Resize(headroom, 0)
		_, _ = response.Write(packet.Payload.Bytes())
		_ = conn.WritePacket(response, destination)
	})
	defer dispatcher.RemoveRay()
	packet := sbuf.NewPacket()
	defer packet.Release()
	for {
		packet.Reset()
		dest, err := conn.ReadPacket(packet)
		if err != nil {
			return
		}
		target := xnet.UDPDestination(xnet.ParseAddress(dest.AddrString()), xnet.Port(dest.Port))
		payload := buf.FromBytes(append([]byte(nil), packet.Bytes()...))
		payload.UDP = &target
		dispatcher.Dispatch(ctx, target, payload)
	}
}
