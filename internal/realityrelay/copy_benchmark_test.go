package realityrelay

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// BenchmarkForwarding 只比较首包选路后的 TCP 搬运，不代表跨机 Reality 或生产吞吐。
// 客户端、前置和回显目标在同一个测试进程；测试临时身份不会写入磁盘。
func BenchmarkForwarding(b *testing.B) {
	for _, mode := range []string{"auto", "buffered"} {
		b.Run(mode, func(b *testing.B) {
			cfg, private, sid := testConfig(b)
			cfg.CopyMode = mode
			prefix := tlsRecords(testHello(b, private, sid, true, time.Now()), 16384)
			backend, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				b.Fatal(err)
			}
			defer backend.Close()
			go func() {
				for {
					c, err := backend.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						_ = c.SetDeadline(time.Now().Add(time.Minute))
						if _, err := io.CopyN(io.Discard, c, int64(len(prefix))); err != nil {
							return
						}
						_, _ = io.Copy(c, c)
					}()
				}
			}()
			cfg.Routes[0].Target = backend.Addr().String()
			server, err := New(cfg)
			if err != nil {
				b.Fatal(err)
			}
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				b.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- server.Serve(context.Background(), listener) }()
			defer func() {
				_ = server.Close()
				if err := <-done; err != nil {
					b.Error(err)
				}
			}()
			const size = 32 << 20
			block := bytes.Repeat([]byte("relay-copy-benchmark"), 4096)
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				conn, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
				if err != nil {
					b.Fatal(err)
				}
				_ = conn.SetDeadline(time.Now().Add(time.Minute))
				if _, err := writeAll(conn, prefix); err != nil {
					conn.Close()
					b.Fatal(err)
				}
				written := make(chan error, 1)
				go func() {
					remaining := size
					for remaining > 0 {
						n := min(remaining, len(block))
						if _, err := writeAll(conn, block[:n]); err != nil {
							written <- err
							return
						}
						remaining -= n
					}
					written <- conn.CloseWrite()
				}()
				n, readErr := io.Copy(io.Discard, conn)
				writeErr := <-written
				_ = conn.Close()
				if readErr != nil || writeErr != nil || n != size {
					b.Fatal("TCP 对照传输未完整完成")
				}
			}
			b.StopTimer()
		})
	}
}
