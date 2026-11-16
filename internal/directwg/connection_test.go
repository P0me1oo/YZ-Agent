package directwg

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type writeResultConn struct {
	net.Conn
	written int
	partial bool
	after   func()
}

func (c *writeResultConn) Write(p []byte) (int, error) {
	c.written = len(p)
	if c.partial {
		c.written /= 2
	}
	c.after()
	if c.partial {
		return c.written, net.ErrClosed
	}
	return c.written, nil
}

type writeResultPacketConn struct {
	N.PacketConn
	written int
	fail    bool
	after   func()
}

func (c *writeResultPacketConn) WritePacket(p *buf.Buffer, _ M.Socksaddr) error {
	defer p.Release()
	if c.fail {
		return net.ErrClosed
	}
	c.written = p.Len()
	c.after()
	return nil
}

func TestSuccessfulDownloadSurvivesUserRevocation(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		for _, change := range []string{"remove", "rotate", "expire", "retire"} {
			t.Run(network+"/"+change, func(t *testing.T) {
				state := newState()
				user := stateUser(t)
				state.replaceUsers([]model.UserSpec{user})
				owner := state.users[user.ID]
				revoke := func() {
					switch change {
					case "rotate":
						state.replaceUsers([]model.UserSpec{stateUser(t)})
					case "expire":
						user.WireGuard.ExpiresAt = time.Now().Unix() - 1
						state.replaceUsers([]model.UserSpec{user})
					default:
						state.replaceUsers(nil)
					}
					if change == "retire" {
						state.mu.Lock()
						delete(state.totals, user.ID)
						state.mu.Unlock()
					}
				}
				payload := []byte("successful-download")
				if network == "tcp" {
					socket := &writeResultConn{after: revoke, partial: true}
					conn := &limitedConn{
						Conn: socket, state: state, owner: owner, id: user.ID, ctx: t.Context(),
					}
					n, err := conn.Write(payload)
					if n != len(payload)/2 || !errors.Is(err, net.ErrClosed) {
						t.Fatalf("部分写入结果被改变: n=%d err=%v", n, err)
					}
					payload = payload[:n]
				} else {
					socket := &writeResultPacketConn{after: revoke}
					conn := &limitedPacketConn{
						PacketConn: socket, state: state, owner: owner, id: user.ID, ctx: t.Context(),
					}
					if err := conn.WritePacket(buf.As(payload), M.ParseSocksaddr("203.0.113.7:53")); err != nil {
						t.Fatal(err)
					}
					if socket.written != len(payload) {
						t.Fatal("UDP 报文未成功写出")
					}
				}
				traffic, _, _ := state.snapshot()
				if got := traffic[user.ID]; got != [2]int64{0, int64(len(payload))} {
					t.Fatalf("撤权丢失成功写出的下载: got=%v want=%d", got, len(payload))
				}
			})
		}
	}
}

func TestDownloadRejectedBeforeWriteIsNotCounted(t *testing.T) {
	state := newState()
	user := stateUser(t)
	state.replaceUsers([]model.UserSpec{user})
	owner := state.users[user.ID]
	state.replaceUsers(nil)
	socket := &writeResultConn{after: func() { t.Fatal("撤权后仍调用写入") }}
	conn := &limitedConn{Conn: socket, state: state, owner: owner, id: user.ID, ctx: context.Background()}
	if n, err := conn.Write([]byte("denied")); n != 0 || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("撤权未阻止写入: n=%d err=%v", n, err)
	}
	traffic, _, _ := state.snapshot()
	if len(traffic) != 0 {
		t.Fatalf("被拒绝的下载被计费: %v", traffic)
	}
}

func TestFailedPacketDownloadIsNotCounted(t *testing.T) {
	state := newState()
	user := stateUser(t)
	state.replaceUsers([]model.UserSpec{user})
	socket := &writeResultPacketConn{fail: true}
	conn := &limitedPacketConn{
		PacketConn: socket, state: state, owner: state.users[user.ID], id: user.ID, ctx: t.Context(),
	}
	if err := conn.WritePacket(buf.As([]byte("failed")), M.ParseSocksaddr("203.0.113.7:53")); err == nil {
		t.Fatal("丢失 UDP 写入错误")
	}
	traffic, _, _ := state.snapshot()
	if len(traffic) != 0 {
		t.Fatalf("写入失败的 UDP 被计费: %v", traffic)
	}
}
