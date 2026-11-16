package singbox

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/miekg/dns"
	singM "github.com/sagernet/sing/common/metadata"
)

func dnsAccountingConfig(t *testing.T) (config.KernelConfig, *atomic.Int64) {
	t.Helper()
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	queries := new(atomic.Int64)
	server := &dns.Server{
		PacketConn: listener,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
			queries.Add(1)
			response := new(dns.Msg)
			response.SetReply(query)
			response.Authoritative = true
			response.Answer = []dns.RR{&dns.A{
				Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP("203.0.113.7"),
			}}
			_ = w.WriteMsg(response)
		}),
	}
	started := make(chan struct{})
	server.NotifyStartedFunc = func() { close(started) }
	go func() { _ = server.ActivateAndServe() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = listener.Close()
		t.Fatal("测试 DNS 服务未就绪")
	}
	t.Cleanup(func() { _ = server.Shutdown() })
	dir := t.TempDir()
	data, err := json.Marshal(M{"dns": M{
		"servers": []M{{
			"type": "udp", "tag": "test-dns", "server": "127.0.0.1",
			"server_port": listener.LocalAddr().(*net.UDPAddr).Port,
		}},
		"final": "test-dns",
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dns.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return config.KernelConfig{Type: "singbox", LogLevel: "fatal", ConfigDir: dir, CustomConfig: path}, queries
}

func TestDNSHijackCountsTCPUDPAndCachedReplies(t *testing.T) {
	for _, protocol := range []string{"vless", "shadowsocks"} {
		for _, network := range []string{"tcp", "udp"} {
			t.Run(protocol+"/"+network, func(t *testing.T) {
				node := runtimeNode(t, protocol)
				node.CustomRoutes = []M{{"port": 53, "action": "hijack-dns"}}
				user := runtimeUser(t, 9201)
				cfg, queries := dnsAccountingConfig(t)
				core := New(cfg)
				if err := core.Start(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(core.Stop)
				client := runtimeClient(t, node, user)
				target := singM.ParseSocksaddr("203.0.113.53:53")
				var tcp net.Conn
				var udp net.PacketConn
				if network == "tcp" {
					tcp = runtimeDial(t, client, target)
				} else {
					udp = runtimeUDPDial(t, client, target)
				}
				var want [2]int64
				for cycle := range 3 {
					query := new(dns.Msg)
					query.SetQuestion("traffic-accounting.invalid.", dns.TypeA)
					request, err := query.Pack()
					if err != nil {
						t.Fatal(err)
					}
					response := make([]byte, 4096)
					var up, down int
					if network == "udp" {
						_ = udp.SetDeadline(time.Now().Add(2 * time.Second))
						up, err = udp.WriteTo(request, target.UDPAddr())
						if err != nil {
							t.Fatal(err)
						}
						down, _, err = udp.ReadFrom(response)
						if err != nil {
							t.Fatal(err)
						}
						response = response[:down]
					} else {
						_ = tcp.SetDeadline(time.Now().Add(2 * time.Second))
						framed := make([]byte, 2+len(request))
						binary.BigEndian.PutUint16(framed, uint16(len(request)))
						copy(framed[2:], request)
						up, err = tcp.Write(framed)
						if err != nil {
							t.Fatal(err)
						}
						var header [2]byte
						if _, err := io.ReadFull(tcp, header[:]); err != nil {
							t.Fatal(err)
						}
						response = response[:int(binary.BigEndian.Uint16(header[:]))]
						if _, err := io.ReadFull(tcp, response); err != nil {
							t.Fatal(err)
						}
						down = len(response) + 2
					}
					answer := new(dns.Msg)
					if err := answer.Unpack(response); err != nil {
						t.Fatal(err)
					}
					if len(answer.Answer) != 1 || answer.Id != query.Id {
						t.Fatal("DNS 应答不正确")
					}
					want[0] += int64(up)
					want[1] += int64(down)
					runtimeWaitTraffic(t, core, user.ID, want)
					_, alive, connections, err := core.GetUserTraffic(context.Background())
					if err != nil || connections != 1 || !alive[user.ID]["127.0.0.1"] {
						t.Fatalf("DNS 会话未进入用户连接统计: alive=%v connections=%d err=%v", alive, connections, err)
					}
					if cycle == 1 {
						for range 2 {
							if err := core.Reload(node, []model.UserSpec{user}, kernel.TLSCert{}); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				traffic, _, _, _ := core.GetUserTraffic(context.Background())
				if traffic[user.ID] != want {
					t.Fatalf("重复读取改变 DNS 累计: %v", traffic)
				}
				if got := queries.Load(); got != 1 {
					t.Fatalf("重复请求未使用 DNS 缓存，不能验证缓存应答计费: upstream=%d", got)
				}
			})
		}
	}
}
