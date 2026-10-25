package balancer

import (
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
)

func testBackend(t *testing.T, identity byte) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &proxyproto.Listener{Listener: listener, ReadHeaderTimeout: time.Second}
	go func() {
		for {
			conn, err := wrapped.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				data := make([]byte, 4)
				if _, err := io.ReadFull(conn, data); err != nil {
					return
				}
				fmt.Fprintf(conn, "%c|%s|%s\n", identity, conn.RemoteAddr(), data)
			}()
		}
	}()
	return listener.Addr().String(), func() { wrapped.Close() }
}

func TestRoundRobinPreservesSourceAndHandlesOfflineTargets(t *testing.T) {
	a, stopA := testBackend(t, 'A')
	defer stopA()
	b, stopB := testBackend(t, 'B')
	defer stopB()
	targets := []Target{{1, a, 1, true}, {2, b, 1, true}}
	r, err := Start("127.0.0.1:0", Config{Network: "tcp", Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	request := func(expected byte) {
		conn, err := net.Dial("tcp", r.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		conn.Write([]byte("test"))
		buffer := make([]byte, 256)
		n, err := conn.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		answer := strings.Split(strings.TrimSpace(string(buffer[:n])), "|")
		if len(answer) != 3 || answer[0] != string(expected) || answer[1] != conn.LocalAddr().String() || answer[2] != "test" {
			t.Fatal("分配或真实来源丢失")
		}
	}
	request('A')
	request('B')
	request('A')
	targets[0].Online = false
	if err := r.UpdateTargets(targets); err != nil {
		t.Fatal(err)
	}
	request('B')
	targets[1].Online = false
	r.UpdateTargets(targets)
	conn, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	conn.Write([]byte("test"))
	buffer := make([]byte, 1)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("全部离线时不应直接出网")
	}
}

func TestConnectionFailureFallsBackWithoutReplayingClientPayload(t *testing.T) {
	backend, stop := testBackend(t, 'B')
	defer stop()
	unused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := unused.Addr().String()
	unused.Close()
	r, err := Start("127.0.0.1:0", Config{Network: "tcp", Targets: []Target{{1, address, 1, true}, {2, backend, 2, true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	conn, err := net.Dial("tcp", r.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	conn.Write([]byte("test"))
	buffer := make([]byte, 256)
	n, err := conn.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(buffer[:n]), "B|") {
		t.Fatal("连接错误未进入备用成员")
	}
}

func TestUnsupportedProtocolCannotStart(t *testing.T) {
	if r, err := Start("127.0.0.1:0", Config{Network: "udp"}); err == nil {
		r.Close()
		t.Fatal("第一版不应开放 UDP")
	}
}
