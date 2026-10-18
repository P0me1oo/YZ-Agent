package singbox

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	singM "github.com/sagernet/sing/common/metadata"
	"testing"
)

func TestAccessLogTrackerDomainIPAndRelayLanding(t *testing.T) {
	r := &accesslog.Recorder{}
	r.Enable(true)
	tracker := NewConnTracker(0)
	tracker.accessLog = r
	tracker.SetUserMap(map[string]int{"access-user": 5})
	metadata := testInboundContext("access-user", "203.0.113.1")
	metadata.Destination = singM.ParseSocksaddr("198.51.100.10:443")
	metadata.Domain = "Example.TEST"
	tcp := tracker.RoutedConnection(context.Background(), &testConn{}, metadata, nil, nil)
	_ = tcp.Close()
	metadata.Domain = ""
	metadata.Destination = singM.ParseSocksaddr("[2001:db8::1]:53")
	udp := tracker.RoutedPacketConnection(context.Background(), &counterTestPacketConn{}, metadata, nil, nil)
	_ = udp.Close()
	metadata.Inbound = relayLandingInboundTag
	tracker.recordAccess(5, metadata, nil)
	batch, err := r.Next()
	if err != nil || batch == nil || len(batch.Records) != 2 {
		t.Fatal("记录数量或落地去重不正确")
	}
	if batch.Records[0].Destination != "example.test" || batch.Records[1].Destination != "2001:db8::1" || batch.Records[1].Port != 53 {
		t.Fatal("目标或端口不正确")
	}
}
