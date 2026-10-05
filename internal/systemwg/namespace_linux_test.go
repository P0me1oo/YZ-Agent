//go:build linux

package systemwg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"testing"

	"github.com/vishvananda/netns"
)

// 并发切换到不同网络空间后，同地址端口可同时监听，宿主空间不受影响。
func TestNamespaceIsolationAndErrors(t *testing.T) {
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	results := make(chan error, 2)
	for range 2 {
		go func() {
			nsResult := make(chan netns.NsHandle, 1)
			go func() { runtime.LockOSThread(); ns, _ := netns.New(); nsResult <- ns }()
			ns := <-nsResult
			if !ns.IsOpen() {
				results <- errors.New("无法创建测试网络空间")
				return
			}
			defer ns.Close()
			path := fmt.Sprintf("/proc/self/fd/%d", ns)
			for range 10 {
				err := InNamespace(context.Background(), path, func() error {
					current, err := netns.Get()
					if err != nil {
						return err
					}
					defer current.Close()
					if !current.Equal(ns) {
						return errors.New("套接字创建前网络空间错误")
					}
					listener, err := net.Listen("tcp4", "0.0.0.0:1080")
					if err != nil {
						return err
					}
					return listener.Close()
				})
				if err != nil {
					results <- err
					return
				}
				current, err := netns.Get()
				if err != nil {
					results <- err
					return
				}
				ok := current.Equal(original)
				current.Close()
				if !ok {
					results <- errors.New("调用方进入了错误网络空间")
					return
				}
			}
			results <- nil
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	called := false
	callback := func() error { called = true; return nil }
	if err := InNamespace(context.Background(), "/nonexistent/yz-wg-test", callback); err == nil || called {
		t.Fatal("无效空间未拒绝")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := InNamespace(ctx, "/proc/self/ns/net", callback); !errors.Is(err, context.Canceled) || called {
		t.Fatal("取消后仍执行回调")
	}
	r := &Runtime{xrayLanding: "/proc/self/ns/net"}
	r.Close()
	if err := r.XrayNamespaceCall(context.Background(), "", callback); err == nil || called {
		t.Fatal("关闭后仍执行回调")
	}
	if err := r.Start(); err == nil {
		t.Fatal("关闭后仍允许启动")
	}
}
