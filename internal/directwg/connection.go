package directwg

import (
	"context"
	"net"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// 与其他代理协议一致，计费统计转发的有效负载，不计握手、保活和 IP 包头。
type limitedConn struct {
	net.Conn
	state  *State
	owner  *userState
	id     int
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *limitedConn) Close() error { c.cancel(); return c.Conn.Close() }
func (c *limitedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && (!c.state.wait(c.ctx, c.id, n, c.owner) || !c.state.transfer(c.id, 0, n, c.owner)) {
		return 0, net.ErrClosed
	}
	return n, err
}
func (c *limitedConn) Write(p []byte) (int, error) {
	if !c.state.wait(c.ctx, c.id, len(p), c.owner) {
		return 0, net.ErrClosed
	}
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.state.transfer(c.id, 1, n, c.owner)
	}
	return n, err
}

type limitedPacketConn struct {
	N.PacketConn
	state  *State
	owner  *userState
	id     int
	ctx    context.Context
	cancel context.CancelFunc
}

func (c *limitedPacketConn) Close() error { c.cancel(); return c.PacketConn.Close() }
func (c *limitedPacketConn) ReadPacket(p *buf.Buffer) (M.Socksaddr, error) {
	dest, err := c.PacketConn.ReadPacket(p)
	if err == nil && (!c.state.wait(c.ctx, c.id, p.Len(), c.owner) || !c.state.transfer(c.id, 0, p.Len(), c.owner)) {
		return M.Socksaddr{}, net.ErrClosed
	}
	return dest, err
}
func (c *limitedPacketConn) WritePacket(p *buf.Buffer, dest M.Socksaddr) error {
	n := p.Len()
	if !c.state.wait(c.ctx, c.id, n, c.owner) {
		p.Release()
		return net.ErrClosed
	}
	err := c.PacketConn.WritePacket(p, dest)
	if err == nil {
		c.state.transfer(c.id, 1, n, c.owner)
	}
	return err
}
