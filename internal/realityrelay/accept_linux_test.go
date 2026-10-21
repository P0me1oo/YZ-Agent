package realityrelay

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestServerRecoversAfterDescriptorExhaustion(t *testing.T) {
	if os.Getenv("YZ_RELAY_DESCRIPTOR_TEST_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestServerRecoversAfterDescriptorExhaustion$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), "YZ_RELAY_DESCRIPTOR_TEST_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("隔离资源恢复测试失败: %v\n%s", err, output)
		}
		return
	}
	cfg, private, sid := testConfig(t)
	prefix := tlsRecords(testHello(t, private, sid, false, time.Now()), 16384)
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := io.CopyN(io.Discard, conn, int64(len(prefix))); err == nil {
					io.Copy(conn, conn)
				}
			}()
		}
	}()
	t.Cleanup(func() { backend.Close(); <-backendDone; workers.Wait() })
	cfg.Routes[0].Target = backend.Addr().String()
	server, address := startTestServer(t, cfg)
	connect := func() net.Conn {
		t.Helper()
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := writeAll(conn, prefix); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	exchange := func(conn net.Conn) {
		t.Helper()
		if _, err := conn.Write([]byte("echo")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil || string(b) != "echo" {
			t.Fatal("资源压力中存量连接被关闭")
		}
	}
	live := connect()
	exchange(live)
	// 预先创建未连接套接字，描述符耗尽后仍能发起一次新连接。
	trigger, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, unix.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(trigger)
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	limited := original
	limited.Cur = min(original.Cur, 64)
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limited); err != nil {
		t.Fatal(err)
	}
	defer unix.Setrlimit(unix.RLIMIT_NOFILE, &original)
	var files []*os.File
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for {
		file, err := os.Open("/dev/null")
		if err != nil {
			break
		}
		files = append(files, file)
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	portNumber, _ := strconv.Atoi(port)
	if err := unix.Connect(trigger, &unix.SockaddrInet4{Port: portNumber, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	exchange(live)
	for _, file := range files {
		file.Close()
	}
	files = nil
	waitFor(t, func() bool { return server.Stats().Accepted >= 2 })
	exchange(connect())
	exchange(live)
}
