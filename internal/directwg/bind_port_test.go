package directwg

import (
	"net"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestBindKeepsConfiguredPortAfterFailure(t *testing.T) {
	occupied, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.LocalAddr().(*net.UDPAddr).Port
	bind := &udpBind{runtime: &Runtime{config: &model.NodeSpec{ListenIP: "127.0.0.1", ServerPort: port}}}
	defer bind.Close()
	if _, _, err = bind.Open(uint16(port)); err == nil {
		t.Fatal("占用中的配置端口不应打开成功")
	}
	// WireGuard 绑定失败会将内部端口归零，随后的启用不能改用随机端口。
	if _, actual, err := bind.Open(0); err == nil {
		t.Fatalf("绑定失败后退回随机端口 %d", actual)
	}
	if bind.socket != nil {
		t.Fatal("失败重试留下了监听")
	}
	if err = occupied.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, actual, err := bind.Open(uint16(port))
		if err != nil || int(actual) != port {
			t.Fatalf("配置端口释放后无法恢复：port=%d err=%v", actual, err)
		}
		if err = bind.Close(); err != nil {
			t.Fatal(err)
		}
		if err = bind.Close(); err != nil {
			t.Fatal("重复关闭失败：", err)
		}
	}
}
