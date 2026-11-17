//go:build linux

package healthcheck

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestListenerReadsThreadNetworkNamespace(t *testing.T) {
	result := make(chan error, 1)
	go func() { result <- probeListenerNamespace(t.Context()) }()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func probeListenerNamespace(ctx context.Context) (result error) {
	runtime.LockOSThread()
	unlock := true
	defer func() {
		if unlock {
			runtime.UnlockOSThread()
		}
	}()
	// 保持主线程的网络空间不变，让另一个锁定线程创建实际监听。
	if unix.Gettid() == os.Getpid() {
		child := make(chan error, 1)
		go func() { child <- probeListenerNamespace(ctx) }()
		return <-child
	}
	original, err := netns.Get()
	if err != nil {
		return err
	}
	defer original.Close()
	namespace, err := netns.New()
	if err != nil {
		return err
	}
	defer namespace.Close()
	defer func() {
		if err := netns.Set(original); err != nil {
			unlock = false
			result = fmt.Errorf("恢复测试网络空间失败：%w", err)
		}
	}()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{Port: 0})
	if err != nil {
		return err
	}
	defer listener.Close()
	if ok, err := Listener(ctx, listener.Addr().String()); err != nil || !ok {
		return fmt.Errorf("未识别当前线程网络空间内的监听：found=%v error=%v", ok, err)
	}
	connections, err := processConnections(ctx, int32(os.Getpid()))
	if err != nil {
		return err
	}
	for _, connection := range connections {
		if connection.Pid != int32(os.Getpid()) {
			return fmt.Errorf("监听归属被误报为线程编号：%d", connection.Pid)
		}
	}
	return nil
}
