//go:build linux

package systemwg

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/common/listener"
	Mtd "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
	"golang.org/x/sys/unix"
)

func testPair(t *testing.T) (*model.RelayWireGuardConfig, *model.RelayWireGuardConfig) {
	t.Helper()
	a, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString
	return &model.RelayWireGuardConfig{PrivateKey: enc(a.Bytes()), PeerPublicKey: enc(b.PublicKey().Bytes()), Address: []string{"10.253.0.1/32"}, AllowedIPs: []string{"0.0.0.0/0"}, MTU: 1380},
		&model.RelayWireGuardConfig{PrivateKey: enc(b.Bytes()), PeerPublicKey: enc(a.PublicKey().Bytes()), Address: []string{"10.253.0.2/32"}, AllowedIPs: []string{"10.253.0.1/32"}, MTU: 1380}
}

// 真正创建两个网络空间和 WG 设备，检查 TCP 使用 BBR、UDP 往返及重复回收。
// 必须在具备网络空间权限和 TUN 的 Linux 测试环境运行，不跳过环境失败。
func TestSystemTCPAndUDP(t *testing.T) {
	entry, landing := testPair(t)
	u, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := u.LocalAddr().(*net.UDPAddr).Port
	_ = u.Close()
	r := &Runtime{}
	defer r.Close()
	b, err := newTunnel(landing, "", port)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnels = append(r.tunnels, b)
	a, err := newTunnel(entry, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 0)
	if err != nil {
		t.Fatal(err)
	}
	r.tunnels = append(r.tunnels, a)
	if err := r.bridge(M{"type": "socks", "listen": "10.253.0.2", "listen_port": 1080, "netns": b.path()}, M{"type": "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dialer := &namespaceDialer{path: a.path()}
	c, err := dialer.DialContext(ctx, "tcp", Mtd.ParseSocksaddr("10.253.0.2:1080"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var algorithm string
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		algorithm, sockErr = unix.GetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION)
	}); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if sockErr != nil || algorithm != "bbr" {
		t.Fatalf("系统 TCP 未使用 BBR: %q %v", algorithm, sockErr)
	}
	client := socks.NewClient(dialer, Mtd.ParseSocksaddr("10.253.0.2:1080"), socks.Version5, "", "")
	tcpEcho, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpEcho.Close()
	go func() {
		c, err := tcpEcho.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	c, err = client.DialContext(ctx, "tcp", Mtd.ParseSocksaddr(tcpEcho.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	payload := bytes.Repeat([]byte("system-tcp"), 4096)
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(c, received); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if !bytes.Equal(payload, received) {
		t.Fatal("TCP 内容不同")
	}
	udpEcho, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udpEcho.Close()
	go func() {
		var buf [2048]byte
		n, addr, err := udpEcho.ReadFrom(buf[:])
		if err == nil {
			_, _ = udpEcho.WriteTo(buf[:n], addr)
		}
	}()
	c, err = client.DialContext(ctx, "udp", Mtd.ParseSocksaddr(udpEcho.LocalAddr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("udp-check")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "udp-check" {
		t.Fatal("UDP 内容不同")
	}
	r.Close()
	r.Close()
}

type namespaceDialer struct{ path string }

func (d *namespaceDialer) DialContext(ctx context.Context, network string, destination Mtd.Socksaddr) (net.Conn, error) {
	return listener.ListenNetworkNamespace[net.Conn](ctx, d.path, func() (net.Conn, error) { return N.SystemDialer.DialContext(ctx, network, destination) })
}
func (d *namespaceDialer) ListenPacket(ctx context.Context, destination Mtd.Socksaddr) (net.PacketConn, error) {
	return listener.ListenNetworkNamespace[net.PacketConn](ctx, d.path, func() (net.PacketConn, error) { return N.SystemDialer.ListenPacket(ctx, destination) })
}

func TestInvalidKeyDoesNotAllocateNamespace(t *testing.T) {
	a, _ := testPair(t)
	a.PrivateKey = "invalid-test-key"
	if _, err := newTunnel(a, "", 0); err == nil {
		t.Fatal("错误密钥未拒绝")
	}
}

// 模拟转接端口被占用：启动失败必须回收网络空间，释放端口后同配置可恢复。
func TestStartFailureClosesNamespaceAndCanRecover(t *testing.T) {
	_, landing := testPair(t)
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	failed, err := newTunnel(landing, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{tunnels: []*tunnel{failed}}
	defer r.Close()
	in := M{"type": "socks", "listen": "127.0.0.1", "listen_port": port}
	if err := r.bridge(in, M{"type": "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err == nil {
		t.Fatal("端口被占用时未拒绝启动")
	}
	if failed.ns.IsOpen() {
		t.Fatal("启动失败后未关闭网络空间描述符")
	}
	r.Close()
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := newTunnel(landing, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	next := &Runtime{tunnels: []*tunnel{recovered}}
	defer next.Close()
	if err := next.bridge(in, M{"type": "direct"}); err != nil {
		t.Fatal(err)
	}
	if err := next.Start(); err != nil {
		t.Fatalf("释放端口后未恢复: %v", err)
	}
}
