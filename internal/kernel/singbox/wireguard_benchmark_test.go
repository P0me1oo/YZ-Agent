//go:build linux && with_quic && with_wireguard && with_wg_benchmark

package singbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	Mtd "github.com/sagernet/sing/common/metadata"
)

// 显式编译此工具后才可执行跨机测试；密钥临时生成到权限为 0600 的专用文件。
// 两端使用相同测试代码，可通过 Go overlay 构建旧运行层作对照。
func TestWireGuardCrossHost(t *testing.T) {
	dir := os.Getenv("WG_BENCH_DIR")
	if dir == "" {
		t.Fatal("必须提供独立测试目录 WG_BENCH_DIR")
	}
	role := os.Getenv("WG_BENCH_ROLE")
	if role == "prepare" {
		a, b := runtimeWireGuardPair(t)
		for name, cfg := range map[string]*model.RelayWireGuardConfig{"entry": a, "landing": b} {
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, name+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if role != "entry" && role != "landing" {
		t.Fatal("WG_BENCH_ROLE 必须为 prepare、entry 或 landing")
	}
	data, err := os.ReadFile(filepath.Join(dir, role+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg model.RelayWireGuardConfig
	if err := json.Unmarshal(data, &wg); err != nil {
		t.Fatal("测试配置无法解析")
	}
	wg.MTU, err = strconv.Atoi(os.Getenv("WG_BENCH_MTU"))
	if err != nil {
		t.Fatal(err)
	}
	kind := os.Getenv("WG_BENCH_CORE")
	if kind != "singbox" && kind != "xray" {
		t.Fatal("必须指定测试核心")
	}
	port, err := strconv.Atoi(os.Getenv("WG_BENCH_PORT"))
	if err != nil || port < 1024 || port > 65535 {
		t.Fatal("必须指定测试端口")
	}
	cfg := config.KernelConfig{Type: kind, LogLevel: "fatal", ConfigDir: t.TempDir()}
	newCore := func() kernel.Kernel {
		if kind == "xray" {
			return xray.New(cfg)
		}
		return New(cfg)
	}
	if role == "landing" {
		// 固定目标只监听本机；隧道里的 SOCKS 由受测运行层提供。
		tcp, udp := runtimeDualListeners(t)
		t.Cleanup(func() { _ = tcp.Close(); _ = udp.Close() })
		go benchmarkServeTCP(tcp)
		go func() {
			b := make([]byte, 65535)
			for {
				n, a, e := udp.ReadFrom(b)
				if e != nil {
					return
				}
				_, _ = udp.WriteTo(b[:n], a)
			}
		}()
		targetPort := tcp.Addr().(*net.TCPAddr).Port
		if kind == "xray" {
			cfg.CustomOutbound = []map[string]any{{"protocol": "freedom", "tag": "direct", "settings": map[string]any{
				"redirect": tcp.Addr().String(), "finalRules": []map[string]any{{"action": "allow", "network": "tcp,udp", "ip": []string{"127.0.0.1/32"}, "port": strconv.Itoa(targetPort)}},
			}}}
		}
		node := runtimeNode(t, "wireguard")
		node.CustomRoutes = nil
		node.ServerPort, node.UpMbps, node.DownMbps = port, 0, 0
		node.Relay = &model.RelayConfig{Mode: "landing", Protocol: "wireguard", WireGuard: &wg}
		if kind == "singbox" {
			node.CustomRoutes = []map[string]any{{"action": "route", "outbound": "direct", "override_address": "127.0.0.1", "override_port": targetPort}}
		}
		core := newCore()
		if err := core.Start(node, nil, kernel.TLSCert{}); err != nil {
			t.Fatal(err)
		}
		defer core.Stop()
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(filepath.Join(dir, "ready"))
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
		<-ctx.Done()
		return
	}
	node := runtimeNode(t, "vless")
	node.CustomRoutes = nil
	node.UpMbps, node.DownMbps = 0, 0
	node.Relay = &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{{NodeID: 7, RouteID: 12, Tag: "relay-7", Protocol: "wireguard", Address: os.Getenv("WG_BENCH_ENDPOINT"), Port: port, WireGuard: &wg}}}
	user := runtimeUser(t, 987)
	core := newCore()
	if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
		t.Fatal(err)
	}
	defer core.Stop()
	user.UUID = relayCredential(user, 12)
	client := runtimeClient(t, node, user)
	target := Mtd.ParseSocksaddr("198.51.100.10:80")
	// 每个方向、每个并发数传输相同总量，收到应答后才记为成功。
	for _, direction := range []byte{'D', 'U'} {
		for _, parallel := range []int{1, 4} {
			var usage syscall.Rusage
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
			beforeCPU := benchmarkCPU(usage)
			start := time.Now()
			var total atomic.Int64
			failures := make(chan error, parallel)
			var workers sync.WaitGroup
			for range parallel {
				workers.Add(1)
				go func() {
					defer workers.Done()
					ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
					defer cancel()
					c, err := client.DialContext(ctx, "tcp", target)
					if err != nil {
						failures <- err
						return
					}
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(25 * time.Second))
					count := int64(128 * 1024 * 1024 / parallel)
					header := make([]byte, 9)
					header[0] = direction
					binary.BigEndian.PutUint64(header[1:], uint64(count))
					if _, err = c.Write(header); err != nil {
						failures <- err
						return
					}
					var n int64
					if direction == 'D' {
						n, err = io.CopyN(io.Discard, c, count)
					} else {
						n, err = io.CopyN(c, benchmarkZero{}, count)
						if err == nil {
							var ack [1]byte
							_, err = io.ReadFull(c, ack[:])
							if err == nil && ack[0] != 'K' {
								err = fmt.Errorf("上传应答不匹配")
							}
						}
					}
					total.Add(n)
					if err != nil {
						failures <- err
					}
				}()
			}
			workers.Wait()
			elapsed := time.Since(start).Seconds()
			_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
			close(failures)
			failed := 0
			for range failures {
				failed++
			}
			fmt.Printf("RESULT core=%s mtu=%d direction=%c streams=%d seconds=%.3f mbps=%.2f cpu_seconds=%.3f failures=%d\n", kind, wg.MTU, direction, parallel, elapsed, float64(total.Load())*8/elapsed/1e6, benchmarkCPU(usage)-beforeCPU, failed)
			if failed != 0 {
				t.Fail()
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	udp, err := client.ListenPacket(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	payload, response := make([]byte, 1200), make([]byte, 65535)
	lost := 0
	for i := range 100 {
		binary.BigEndian.PutUint32(payload, uint32(i))
		_ = udp.SetDeadline(time.Now().Add(time.Second))
		if _, err := udp.WriteTo(payload, target.UDPAddr()); err != nil {
			lost++
			continue
		}
		n, _, err := udp.ReadFrom(response)
		if err != nil || n != len(payload) || binary.BigEndian.Uint32(response) != uint32(i) {
			lost++
		}
	}
	fmt.Printf("RESULT core=%s mtu=%d udp_packets=100 udp_lost=%d\n", kind, wg.MTU, lost)
	if lost != 0 {
		t.Fail()
	}
}

type benchmarkZero struct{}

func (benchmarkZero) Read(b []byte) (int, error) { clear(b); return len(b), nil }
func benchmarkCPU(r syscall.Rusage) float64 {
	return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
}
func benchmarkServeTCP(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			var header [9]byte
			if _, err := io.ReadFull(c, header[:]); err != nil {
				return
			}
			n := int64(binary.BigEndian.Uint64(header[1:]))
			if n < 0 || n > 1<<30 {
				return
			}
			if header[0] == 'D' {
				_, _ = io.CopyN(c, benchmarkZero{}, n)
			} else if header[0] == 'U' {
				if _, err := io.CopyN(io.Discard, c, n); err == nil {
					_, _ = c.Write([]byte{'K'})
				}
			}
		}()
	}
}
