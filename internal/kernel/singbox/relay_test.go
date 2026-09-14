package singbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing-box/adapter"
	singBufio "github.com/sagernet/sing/common/bufio"
	"golang.org/x/time/rate"
)

func testRelayNode() *model.NodeSpec {
	return &model.NodeSpec{Protocol: "vless", Network: "tcp", ListenIP: "127.0.0.1", ServerPort: 24443,
		Relay: &model.RelayConfig{Mode: "entry", RouteID: 11, Children: []model.RelayChild{
			{NodeID: 7, RouteID: 12, Tag: "relay-7", Protocol: "shadowsocks", Address: "127.0.0.1", Port: 28388, Cipher: "aes-128-gcm"},
		}}}
}

func TestRelayIdentityMappingAndCollision(t *testing.T) {
	node := testRelayNode()
	user := runtimeUser(t, 1001)
	if err := validateRelayUsers(node, []model.UserSpec{user}); err != nil {
		t.Fatal(err)
	}
	other := user
	other.ID++
	other.UUID = relayCredential(user, 99)
	if validateRelayUsers(node, []model.UserSpec{user, other}) == nil {
		t.Fatal("路由字节覆盖后的身份碰撞必须拒绝")
	}
	if validateRelayUsers(node, []model.UserSpec{user, user}) == nil {
		t.Fatal("重复用户 ID 必须拒绝")
	}
	other = runtimeUser(t, user.ID)
	if relayUserName(user, 12) == relayUserName(other, 12) {
		t.Fatal("凭据轮换必须使旧会话身份失效")
	}
	if strings.Contains(relayUserName(user, 12), user.UUID) {
		t.Fatal("内部名称泄露认证凭据")
	}
	tracker := NewConnTracker(0)
	tracker.setNodeUsers(node, []model.UserSpec{user})
	for _, route := range []int{11, 12} {
		canonical, id, stats, _, allowed := tracker.resolveUser(relayUserName(user, route), nil)
		if canonical != user.UUID || id != user.ID || stats == nil || !allowed {
			t.Fatal("线路身份没有映射回原用户")
		}
	}
	tracker.setNodeUsers(node, []model.UserSpec{other})
	if _, _, _, _, allowed := tracker.resolveUser(relayUserName(user, 12), nil); allowed {
		t.Fatal("凭据轮换后仍接受旧会话身份")
	}
}

type relayTestOutbound struct {
	adapter.Outbound
	tag string
}

func (o relayTestOutbound) Tag() string { return o.tag }

func TestRelayPacketCountersUseActualOutbound(t *testing.T) {
	node := testRelayNode()
	user := runtimeUser(t, 1002)
	tracker := NewConnTracker(0)
	tracker.setNodeUsers(node, []model.UserSpec{user})
	var seenLimitUUID string
	var seenSpeedUUID string
	tracker.SetDeviceLimitFunc(func(value string) (int, bool) { seenLimitUUID = value; return 1, true })
	tracker.SetSpeedLimitFunc(func(value string) *rate.Limiter { seenSpeedUUID = value; return nil })
	for _, tag := range []string{"relay-7", "direct"} {
		wrapped := tracker.RoutedPacketConnection(context.Background(), &counterTestPacketConn{reads: [][]byte{[]byte("request")}},
			testInboundContext(relayUserName(user, 12), "127.0.0.1"), nil, relayTestOutbound{tag: tag})
		if _, err := singBufio.CopyPacket(&counterTestPacketConn{}, wrapped); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		if _, err := singBufio.CopyPacket(wrapped, &counterTestPacketConn{reads: [][]byte{[]byte("ok")}}); !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		_ = wrapped.Close()
	}
	if seenLimitUUID != user.UUID {
		t.Fatal("设备限制没有沿用真实用户身份")
	}
	if seenSpeedUUID != user.UUID {
		t.Fatal("速度限制没有沿用真实用户身份")
	}
	if got := tracker.traffic.snapshot()[user.ID]; got != [2]int64{14, 4} {
		t.Fatalf("套餐计数错误: %v", got)
	}
	if got := tracker.traffic.relaySnapshot()[7]; got != [2]int64{7, 2} {
		t.Fatalf("实际直连流量被计入落地: %v", got)
	}
	if got := tracker.traffic.relayUserSnapshot()[user.ID][7]; got != [2]int64{7, 2} {
		t.Fatalf("线路明细计数错误: %v", got)
	}
	tracker.setNodeUsers(node, nil)
	if got := tracker.traffic.relaySnapshot()[7]; got != [2]int64{7, 2} {
		t.Fatal("删除用户清空了待上报流量")
	}
}

func BenchmarkSingBoxRelayConfig(b *testing.B) {
	node := testRelayNode()
	for id := 13; id <= 31; id++ {
		child := node.Relay.Children[0]
		child.NodeID, child.RouteID = id, id
		child.Tag = fmt.Sprintf("relay-%d", id)
		node.Relay.Children = append(node.Relay.Children, child)
	}
	users := make([]model.UserSpec, 1000)
	for i := range users {
		id, err := uuid.NewV4()
		if err != nil {
			b.Fatal(err)
		}
		users[i] = model.UserSpec{ID: i + 1, UUID: id.String()}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = mustBuildConfig(b, config.KernelConfig{}, node, users, kernel.TLSCert{})
	}
}
