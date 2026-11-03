package xray

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	xdispatch "github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/bandwidth"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/features/routing"
	xproxy "github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport"
	"golang.org/x/time/rate"
)

type visionPolicy struct{ policy.DefaultManager }

func (visionPolicy) ForLevel(uint32) policy.Session {
	p := policy.SessionDefault()
	p.Stats.UserUplink, p.Stats.UserDownlink = true, true
	return p
}

type visionDispatchProbe struct {
	routing.Dispatcher
	state int
	fail  bool
}

func (d *visionDispatchProbe) observe(ctx context.Context) error {
	d.state = session.InboundFromContext(ctx).CanSpliceCopy
	if d.fail {
		return errors.New("测试调度失败")
	}
	return nil
}
func (d *visionDispatchProbe) Dispatch(ctx context.Context, _ xnet.Destination) (*transport.Link, error) {
	return &transport.Link{Reader: nopReader{}, Writer: buf.Discard}, d.observe(ctx)
}
func (d *visionDispatchProbe) DispatchLink(ctx context.Context, _ xnet.Destination, _ *transport.Link) error {
	return d.observe(ctx)
}

func TestVisionAccountingPreservesSpliceAndDispatchLifecycle(t *testing.T) {
	for _, useLink := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("link=%t/fail=%t", useLink, fail), func(t *testing.T) {
				d := newTestDispatcher()
				d.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
				probe := &visionDispatchProbe{fail: fail}
				d.innerDisp = probe
				ctx := admissionContext()
				session.InboundFromContext(ctx).CanSpliceCopy = 2
				dest := xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443)
				link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
				var err error
				if useLink {
					err = d.DispatchLink(ctx, dest, link)
				} else {
					link, err = d.Dispatch(ctx, dest)
				}
				if fail != (err != nil) || probe.state != 2 {
					t.Fatalf("调度改变了 Vision 快速复制状态: state=%d err=%v", probe.state, err)
				}
				if !fail {
					if err := common.Close(link.Writer); err != nil {
						t.Fatal(err)
					}
					common.Interrupt(link.Writer)
				}
				if d.connCount.Load() != 0 || d.userConnCounter(1).Load() != 0 {
					t.Fatal("关闭或失败后连接名额未归还")
				}
			})
		}
	}
}

type visionObservedWriter struct {
	buf.Writer
	calls int
}

func (w *visionObservedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	w.calls++
	return w.Writer.WriteMultiBuffer(mb)
}

func (w *visionObservedWriter) UnwrapWriter() buf.Writer { return w.Writer }

type visionCopyDispatcher struct {
	routing.Dispatcher
	stats               *trafficStats
	source, destination *net.TCPConn
	cancelLimiter       bool
}

func (d *visionCopyDispatcher) DispatchLink(ctx context.Context, _ xnet.Destination, link *transport.Link) error {
	// 真实核心在 Node 调度后才添加用户计数、中转计数与限速包装。
	xdispatch.WrapLink(ctx, visionPolicy{}, d.stats, link)
	defer common.Close(link.Writer)
	inbound := session.InboundFromContext(ctx)
	// 模拟 VisionWriter 完成协议切换；核心仅允许状态 2 变为 1。
	if inbound.CanSpliceCopy == 2 {
		inbound.CanSpliceCopy = 1
	}
	copyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := signal.CancelAfterInactivity(copyCtx, cancel, time.Minute)
	defer timer.SetTimeout(0)
	if d.cancelLimiter {
		canceled, stop := context.WithCancel(copyCtx)
		stop()
		link.Writer.(*xdispatch.RateLimitWriter).Context = canceled
	}
	return xproxy.CopyRawConnIfExist(copyCtx, d.source, d.destination, link.Writer, timer, nil)
}

func visionTCPPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	for _, conn := range []*net.TCPConn{client, server} {
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return client, server
}

func TestVisionAccountingCopiesEveryByteAndEnforcesLimiter(t *testing.T) {
	const size = 64 * 1024
	for _, route := range []int{0, 3} {
		for _, limited := range []bool{false, true} {
			t.Run(fmt.Sprintf("route=%d/limited=%t", route, limited), func(t *testing.T) {
				manager, err := newTrafficStats(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				inst := new(xcore.Instance)
				bm := bandwidth.New()
				if err := inst.AddFeature(bm); err != nil {
					t.Fatal(err)
				}
				// 无补充令牌，精确验证上传/下载路径是否实际消耗额度。
				limiter := rate.NewLimiter(0, 4*size)
				if limited {
					bm.SetUserLimiter(userEmail(1), limiter)
				}
				x := New(config.KernelConfig{})
				x.instance, x.nodeConfig = inst, trafficRelayConfig(11)
				// 使用真实统计采样；用户与落地映射由生产收集逻辑解析。
				if err := inst.AddFeature(manager); err != nil {
					t.Fatal(err)
				}
				for round := 1; round <= 2; round++ {
					producer, source := visionTCPPair(t)
					destination, consumer := visionTCPPair(t)
					payload := bytes.Repeat([]byte{byte(round)}, size)
					sent := make(chan error, 1)
					go func() {
						_, err := producer.Write(payload)
						if err == nil {
							err = producer.CloseWrite()
						}
						sent <- err
					}()
					received := make(chan error, 1)
					go func() {
						data := make([]byte, size)
						_, err := io.ReadFull(consumer, data)
						if err == nil && !bytes.Equal(data, payload) {
							err = errors.New("内容不一致")
						}
						received <- err
					}()
					ctx := context.WithValue(admissionContext(), xcore.XrayKey(1), inst)
					inbound := session.InboundFromContext(ctx)
					inbound.CanSpliceCopy, inbound.VlessRoute, inbound.Conn = 2, xnet.Port(route), destination
					ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{CanSpliceCopy: 1}})
					observed := &visionObservedWriter{Writer: buf.NewWriter(destination)}
					link := &transport.Link{Reader: buf.NewReader(source), Writer: observed}
					d := newTestDispatcher()
					d.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
					d.innerDisp = &visionCopyDispatcher{stats: manager, source: source, destination: destination}
					if err := d.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 443), link); err != nil {
						t.Fatal(err)
					}
					if err := <-sent; err != nil {
						t.Fatal(err)
					}
					if err := <-received; err != nil {
						t.Fatal(err)
					}
					if (runtime.GOOS == "linux" || runtime.GOOS == "android") && observed.calls != 0 {
						t.Fatal("支持 splice 的 TCP 连接回退了普通复制")
					}
					want := int64(round * size)
					for range 2 {
						traffic, _, _, err := x.GetUserTraffic(t.Context())
						if err != nil || traffic[1] != [2]int64{0, want} {
							t.Fatalf("重复采样用户计数=%v err=%v", traffic, err)
						}
						relay, err := x.GetRelayUserTraffic(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						if route > 0 && relay[1][11] != [2]int64{0, want} {
							t.Fatalf("中转计数=%v", relay)
						}
						if route == 0 && len(relay) != 0 {
							t.Fatalf("普通用户产生中转计数=%v", relay)
						}
					}
					if limited && int64(limiter.Tokens()) != int64(4*size)-want {
						t.Fatalf("限速额度未完整消耗: tokens=%.0f", limiter.Tokens())
					}
					common.Interrupt(link.Writer)
					if d.connCount.Load() != 0 || d.userConnCounter(1).Load() != 0 {
						t.Fatal("重复关闭扣减了连接计数")
					}
				}
			})
		}
	}
}

func TestVisionAccountingCanceledLimiterStopsTransfer(t *testing.T) {
	producer, source := visionTCPPair(t)
	destination, consumer := visionTCPPair(t)
	defer consumer.Close()
	manager, err := newTrafficStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inst := new(xcore.Instance)
	bm := bandwidth.New()
	limiter := rate.NewLimiter(0, 2048)
	if !limiter.AllowN(time.Now(), 2048) {
		t.Fatal("无法准备耗尽的限速额度")
	}
	bm.SetUserLimiter(userEmail(1), limiter)
	if err := inst.AddFeature(bm); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(admissionContext(), xcore.XrayKey(1), inst)
	inbound := session.InboundFromContext(ctx)
	inbound.CanSpliceCopy, inbound.VlessRoute, inbound.Conn = 2, 3, destination
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{CanSpliceCopy: 1}})
	sent := make(chan error, 1)
	go func() { _, err := producer.Write(bytes.Repeat([]byte("x"), 1024)); producer.CloseWrite(); sent <- err }()
	observed := &visionObservedWriter{Writer: buf.NewWriter(destination)}
	link := &transport.Link{Reader: buf.NewReader(source), Writer: observed}
	d := newTestDispatcher()
	d.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
	d.innerDisp = &visionCopyDispatcher{stats: manager, source: source, destination: destination, cancelLimiter: true}
	err = d.DispatchLink(ctx, xnet.TCPDestination(xnet.ParseAddress("127.0.0.1"), 443), link)
	if !errors.Is(err, context.Canceled) || observed.calls != 0 {
		t.Fatalf("取消限速后仍传输: calls=%d err=%v", observed.calls, err)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if got := manager.GetCounter(counterName(1, "downlink")).Value(); got != 0 {
		t.Fatalf("失败传输计数=%d", got)
	}
	common.Interrupt(link.Writer)
	if d.connCount.Load() != 0 || d.userConnCounter(1).Load() != 0 {
		t.Fatal("取消后名额未归还")
	}
}
