package xray

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/transport"
)

type admissionDispatcher struct {
	routing.Dispatcher
	fail              bool
	closeBeforeReturn bool
}

func (s *admissionDispatcher) Dispatch(context.Context, xnet.Destination) (*transport.Link, error) {
	if s.fail {
		return nil, errors.New("测试调度失败")
	}
	return &transport.Link{Reader: nopReader{}, Writer: buf.Discard}, nil
}

func (s *admissionDispatcher) DispatchLink(_ context.Context, _ xnet.Destination, link *transport.Link) error {
	if s.closeBeforeReturn {
		_ = link.Writer.(*closeTrackingWriter).Close()
	}
	if s.fail {
		return errors.New("测试调度失败")
	}
	return nil
}

func admissionContext() context.Context {
	return session.ContextWithInbound(context.Background(), &session.Inbound{
		User:   &protocol.MemoryUser{Email: userEmail(1)},
		Source: xnet.TCPDestination(xnet.ParseAddress("192.0.2.1"), 12345),
	})
}

func TestDispatcherConcurrentAdmission(t *testing.T) {
	for _, useLink := range []bool{false, true} {
		t.Run(map[bool]string{false: "Dispatch", true: "DispatchLink"}[useLink], func(t *testing.T) {
			ld := newTestDispatcher()
			ld.UpdateLimits(map[string]int{userEmail(1): 1}, nil, nil)
			cl := limiter.New()
			cl.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 4}})
			ld.SetConnLimiter(cl)
			ld.innerDisp = &admissionDispatcher{}
			ctx := admissionContext()
			dest := xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443)
			// 全部尝试结束后才关闭成功连接，确保名额不会被提前释放。
			for round := 0; round < 3; round++ {
				cl.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 4}})
				start := make(chan struct{})
				accepted := make(chan *transport.Link, 64)
				var wg sync.WaitGroup
				for i := 0; i < 64; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						var link *transport.Link
						var err error
						if useLink {
							link = &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
							err = ld.DispatchLink(ctx, dest, link)
						} else {
							link, err = ld.Dispatch(ctx, dest)
						}
						if err == nil {
							accepted <- link
						}
					}()
				}
				close(start)
				wg.Wait()
				close(accepted)
				if len(accepted) != 4 {
					t.Fatalf("放行 %d 条，期望 4", len(accepted))
				}
				for link := range accepted {
					writer := link.Writer.(*closeTrackingWriter)
					_ = writer.Close()
					writer.Interrupt()
				}
				if ld.userConnCounter(1).Load() != 0 || ld.connCount.Load() != 0 {
					t.Fatal("关闭后计数未归零")
				}
				if stats := cl.SnapshotLimitEvents()[1]; stats.ConnHits != 60 || stats.PeakConn != 4 {
					t.Fatalf("拒绝统计错误：%+v", stats)
				}
			}
		})
	}
}

func TestDispatcherFailureReleasesAdmission(t *testing.T) {
	for _, useLink := range []bool{false, true} {
		for _, closeFirst := range []bool{false, true} {
			ld := newTestDispatcher()
			ld.UpdateLimits(map[string]int{userEmail(1): 1}, map[string]int{userEmail(1): 1}, nil)
			cl := limiter.New()
			cl.UpdateUsers([]model.UserSpec{{ID: 1, ConnLimit: 1}})
			ld.SetConnLimiter(cl)
			inner := &admissionDispatcher{fail: true, closeBeforeReturn: closeFirst}
			ld.innerDisp = inner
			ctx := admissionContext()
			dest := xnet.TCPDestination(xnet.ParseAddress("192.0.2.2"), 443)
			for i := 0; i < 2; i++ {
				link := &transport.Link{Reader: nopReader{}, Writer: buf.Discard}
				var err error
				if useLink {
					err = ld.DispatchLink(ctx, dest, link)
				} else {
					_, err = ld.Dispatch(ctx, dest)
				}
				if err == nil {
					t.Fatal("应返回调度错误")
				}
				if useLink {
					_ = link.Writer.(*closeTrackingWriter).Close()
					link.Writer.(*closeTrackingWriter).Interrupt()
				}
				ips, count := ld.GetConnectionState()
				if count != 0 || len(ips) != 0 || ld.userConnCounter(1).Load() != 0 {
					t.Fatalf("失败后名额未归还：%v %d", ips, count)
				}
			}
			inner.fail = false
			link, err := ld.Dispatch(ctx, dest)
			if err != nil {
				t.Fatalf("失败恢复后无法连接：%v", err)
			}
			_ = link.Writer.(*closeTrackingWriter).Close()
		}
	}
}
