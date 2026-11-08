package singbox

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestIPConnectionsTrackRealPacketsAndClose(t *testing.T) {
	user := runtimeUser(t, 1005)
	tracker := NewConnTracker(0)
	tracker.setNodeUsers(testRelayNode(), []model.UserSpec{user})
	core := &SingBox{connTracker: tracker}
	sources := []string{"8.8.8.8", "8.8.8.8", "2606:4700:1:2::1", "2606:4700:1:2::2", "127.0.0.1"}
	var closeAll []func() error
	for _, ip := range sources {
		conn := tracker.RoutedPacketConnection(context.Background(), &counterTestPacketConn{},
			testInboundContext(relayUserName(user, 12), ip), nil, relayTestOutbound{tag: "relay-7"})
		closeAll = append(closeAll, conn.Close)
		defer conn.Close()
	}
	counts, err := core.GetIPConnectionSnapshot(context.Background())
	if err != nil || counts[user.ID]["8.8.8.8"] != 2 || len(counts[user.ID]) != 3 || counts[user.ID][sources[2]] != 1 || counts[user.ID][sources[3]] != 1 {
		t.Fatalf("完整 IP 连接未正确拆分: %v %v", counts, err)
	}
	_ = closeAll[0]()
	_ = closeAll[0]()
	current, _ := core.GetIPConnectionSnapshot(context.Background())
	if current[user.ID]["8.8.8.8"] != 1 || counts[user.ID]["8.8.8.8"] != 2 {
		t.Fatal("重复关闭或旧快照被修改")
	}
	for _, close := range closeAll {
		_ = close()
	}
	current, _ = core.GetIPConnectionSnapshot(context.Background())
	if current == nil || len(current) != 0 {
		t.Fatalf("关闭后需显式空快照: %v", current)
	}
}
