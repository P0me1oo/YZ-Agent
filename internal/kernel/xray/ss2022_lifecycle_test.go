package xray

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	singMetadata "github.com/sagernet/sing/common/metadata"
	"github.com/xtls/xray-core/common"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/routing"
	ss2022 "github.com/xtls/xray-core/proxy/shadowsocks_2022"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

type ss2022LifecycleDispatcher struct {
	routing.Dispatcher
	link       *transport.Link
	dispatched chan struct{}
}

func (d *ss2022LifecycleDispatcher) Dispatch(context.Context, xnet.Destination) (*transport.Link, error) {
	close(d.dispatched)
	return d.link, nil
}

// 通过 SS2022 真实握手和入站转发检查释放，不依赖核心的内部桥接模块。
func TestSS2022CopyConnReleasesTrackedSource(t *testing.T) {
	d := newTestDispatcher()
	email := userEmail(1)
	d.UpdateLimits(map[string]int{email: 1}, nil, nil)
	d.checkDeviceLimit(email, "8.8.8.8", true)
	downReader, downWriter := pipe.New()
	upReader, upWriter := pipe.New()
	defer common.Interrupt(downReader)
	defer common.Interrupt(upReader)
	link := &transport.Link{Reader: downReader, Writer: upWriter}
	d.trackLink(link, email, "8.8.8.8", 1, true, nil)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	done := make(chan struct{})
	instance, err := core.New(&core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	ctx := context.WithValue(context.Background(), core.XrayKey(1), instance)
	ctx = session.ContextWithInbound(ctx, &session.Inbound{})
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	password := base64.StdEncoding.EncodeToString(key)
	inbound, err := ss2022.NewServer(ctx, &ss2022.ServerConfig{Method: "2022-blake3-aes-128-gcm", Key: password, Email: email})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &ss2022LifecycleDispatcher{link: link, dispatched: make(chan struct{})}
	go func() {
		_ = inbound.Process(ctx, xnet.Network_TCP, server, dispatcher)
		close(done)
	}()
	method, err := shadowaead_2022.NewWithPassword("2022-blake3-aes-128-gcm", password, nil)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := method.DialEarlyConn(client, singMetadata.ParseSocksaddr("127.0.0.1:443"))
	_ = client.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := wrapped.Write([]byte("lifecycle")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatcher.dispatched:
	case <-time.After(5 * time.Second):
		t.Fatal("SS2022 握手没有进入转发")
	}
	_ = client.Close()
	_ = downWriter.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("两端关闭后复制协程没有退出")
	}
	ips, count := d.GetConnectionState()
	if count != 0 || len(ips[1]) != 0 {
		t.Fatalf("SS2022 已断开但仍占用设备: connections=%d sources=%d", count, len(ips[1]))
	}
}
