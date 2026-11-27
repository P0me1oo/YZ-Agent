package xray

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"time"
	_ "unsafe"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/quicvarint"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	hy "github.com/xtls/xray-core/proxy/hysteria"
	"github.com/xtls/xray-core/proxy/hysteria/account"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	hytransport "github.com/xtls/xray-core/transport/internet/hysteria"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type hysteriaValidatorKey struct{}

func hysteriaCompatibilityContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, hysteriaValidatorKey{}, &atomic.Pointer[account.Validator]{})
}

// The pinned worker recognizes only *hysteria.Server, so restore its validator
// context at the transport boundary when using our embedded server adapter.
//
//go:linkname transportListenerRegistry github.com/xtls/xray-core/transport/internet.transportListenerCache
var transportListenerRegistry map[string]internet.ListenFunc

// sing-quic omits the address after the first fragment. The pinned Xray parser
// rejects those fragments. Adapt UDP only; keep its authenticated transport,
// user manager, TCP path, dispatcher and accounting unchanged.
func init() {
	listener := transportListenerRegistry["hysteria"]
	transportListenerRegistry["hysteria"] = func(ctx context.Context, address xnet.Address, port xnet.Port, settings *internet.MemoryStreamConfig, handler internet.ConnHandler) (internet.Listener, error) {
		if holder, ok := ctx.Value(hysteriaValidatorKey{}).(*atomic.Pointer[account.Validator]); ok && holder.Load() != nil {
			ctx = hytransport.ContextWithValidator(ctx, holder.Load())
		}
		return listener(ctx, address, port, settings, handler)
	}
	typ := reflect.TypeOf((*hy.ServerConfig)(nil))
	original := typeCreatorRegistry[typ]
	typeCreatorRegistry[typ] = func(ctx context.Context, cfg interface{}) (interface{}, error) {
		server, err := original(ctx, cfg)
		if err != nil {
			return nil, err
		}
		typed := server.(*hy.Server)
		holder, ok := ctx.Value(hysteriaValidatorKey{}).(*atomic.Pointer[account.Validator])
		if !ok {
			return server, nil
		}
		holder.Store(typed.HysteriaInboundValidator())
		return &hysteriaFragmentServer{Server: typed}, nil
	}
}

type hysteriaFragmentServer struct{ *hy.Server }

func (s *hysteriaFragmentServer) Process(ctx context.Context, network xnet.Network, conn stat.Connection, dispatcher routing.Dispatcher) error {
	transportConn, ok := stat.TryUnwrapStatsConn(conn).(*hytransport.InterConn)
	if !ok {
		return s.Server.Process(ctx, network, conn, dispatcher)
	}
	user := transportConn.User()
	if user == nil {
		return errors.New("HY2 UDP 缺少认证身份")
	}
	inbound := session.InboundFromContext(ctx)
	inbound.Name, inbound.User = "hysteria", user
	if identity, ok := user.Account.(*account.MemoryAccount); ok {
		inbound.VlessRoute = identity.VR
	}
	reader := &hysteriaFragmentReader{conn: conn, pending: make(map[uint16]*hysteriaFragments)}
	first, err := reader.read()
	if err != nil {
		return err
	}
	reader.first = first
	return dispatcher.DispatchLink(ctx, *first.UDP, &transport.Link{Reader: reader, Writer: &hysteriaFragmentWriter{conn: conn, target: first.UDP.NetAddr()}})
}

type hysteriaFragments struct {
	parts       [][]byte
	address     string
	size, count int
	expires     time.Time
}
type hysteriaFragmentReader struct {
	conn    stat.Connection
	first   *buf.Buffer
	pending map[uint16]*hysteriaFragments
}

func (r *hysteriaFragmentReader) Interrupt()   { _ = r.conn.Close() }
func (r *hysteriaFragmentReader) Close() error { return r.conn.Close() }
func (r *hysteriaFragmentReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.first != nil {
		first := r.first
		r.first = nil
		return buf.MultiBuffer{first}, nil
	}
	packet, err := r.read()
	if err != nil {
		return nil, err
	}
	return buf.MultiBuffer{packet}, nil
}
func (r *hysteriaFragmentReader) read() (*buf.Buffer, error) {
	var wire [65535]byte
	for {
		n, err := r.conn.Read(wire[:])
		if err != nil {
			return nil, err
		}
		packet, err := parseHysteriaFragment(wire[:n])
		if err != nil {
			continue
		}
		data, address := r.feed(packet, time.Now())
		if data == nil {
			continue
		}
		target, err := xnet.ParseDestination("udp:" + address)
		if err != nil {
			continue
		}
		b := buf.FromBytes(data)
		b.UDP = &target
		return b, nil
	}
}
func parseHysteriaFragment(raw []byte) (*hy.UDPMessage, error) {
	if len(raw) < 9 {
		return nil, io.ErrUnexpectedEOF
	}
	m := &hy.UDPMessage{SessionID: binary.BigEndian.Uint32(raw), PacketID: binary.BigEndian.Uint16(raw[4:]), FragID: raw[6], FragCount: raw[7]}
	reader := bytes.NewReader(raw[8:])
	size, err := quicvarint.Read(reader)
	if err != nil || size > 2048 || size >= uint64(reader.Len()) || m.FragCount == 0 || m.FragID >= m.FragCount {
		return nil, errors.New("invalid HY2 UDP fragment")
	}
	addr := make([]byte, int(size))
	io.ReadFull(reader, addr)
	m.Addr = string(addr)
	m.Data = raw[len(raw)-reader.Len():]
	if (m.FragID == 0 && m.Addr == "") || len(m.Data) > 65507 {
		return nil, errors.New("invalid HY2 UDP address or size")
	}
	return m, nil
}
func (r *hysteriaFragmentReader) feed(m *hy.UDPMessage, now time.Time) ([]byte, string) {
	if m.FragCount == 1 {
		return append([]byte(nil), m.Data...), m.Addr
	}
	for id, item := range r.pending {
		if !now.Before(item.expires) {
			delete(r.pending, id)
		}
	}
	item := r.pending[m.PacketID]
	if item == nil {
		if len(r.pending) >= 64 {
			return nil, ""
		}
		item = &hysteriaFragments{parts: make([][]byte, int(m.FragCount)), expires: now.Add(5 * time.Second)}
		r.pending[m.PacketID] = item
	}
	if len(item.parts) != int(m.FragCount) || item.parts[m.FragID] != nil {
		return nil, ""
	}
	if m.Addr != "" {
		if item.address != "" && item.address != m.Addr {
			delete(r.pending, m.PacketID)
			return nil, ""
		}
		item.address = m.Addr
	}
	if item.size+len(m.Data) > 65507 {
		delete(r.pending, m.PacketID)
		return nil, ""
	}
	item.parts[m.FragID] = append([]byte(nil), m.Data...)
	item.size += len(m.Data)
	item.count++
	if item.count != len(item.parts) {
		return nil, ""
	}
	delete(r.pending, m.PacketID)
	if item.address == "" {
		return nil, ""
	}
	data := make([]byte, 0, item.size)
	for _, part := range item.parts {
		data = append(data, part...)
	}
	return data, item.address
}

type hysteriaFragmentWriter struct {
	conn     stat.Connection
	target   string
	packetID uint16
}

func (w *hysteriaFragmentWriter) WriteMultiBuffer(packets buf.MultiBuffer) error {
	defer buf.ReleaseMulti(packets)
	for _, b := range packets {
		target := w.target
		if b.UDP != nil {
			target = b.UDP.NetAddr()
		}
		w.packetID++
		message := hy.UDPMessage{PacketID: w.packetID, FragCount: 1, Addr: target, Data: b.Bytes()}
		send := func(m *hy.UDPMessage) error {
			wire := make([]byte, m.Size())
			n := m.Serialize(wire)
			if n < 0 {
				return errors.New("HY2 UDP response too large")
			}
			_, err := w.conn.Write(wire[:n])
			return err
		}
		if err := send(&message); err != nil {
			var large *quic.DatagramTooLargeError
			if !errors.As(err, &large) {
				return err
			}
			if int(large.MaxDatagramPayloadSize) <= message.HeaderSize() {
				return err
			}
			for _, part := range hy.FragUDPMessage(&message, int(large.MaxDatagramPayloadSize)) {
				if err := send(&part); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
