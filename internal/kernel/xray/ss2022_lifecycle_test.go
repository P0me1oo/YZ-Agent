package xray

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/singbridge"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"
)

// SS2022 使用 singbridge 复制连接；两端关闭后必须归还调度器登记的来源。
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
	go func() {
		_ = singbridge.CopyConn(context.Background(), server, link, server)
		close(done)
	}()
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
