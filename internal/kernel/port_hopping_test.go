package kernel

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestPortHoppingChangeDoesNotRequireKernelRestart(t *testing.T) {
	node := &model.NodeSpec{Protocol: "hysteria", Version: 2, ServerPort: 8443, PortHopping: "20000-20010"}
	initial := ComputeHash(node, nil)
	node.PortHopping = "21000-21100,21300"
	if ComputeHash(node, nil) != initial {
		t.Fatal("只更换外部跳跃端口不应重建内核")
	}
	if node.PortHopping != "21000-21100,21300" {
		t.Fatal("计算内核哈希不能改写待应用的防火墙配置")
	}
	node.ServerPort++
	if ComputeHash(node, nil) == initial {
		t.Fatal("实际监听端口变化必须重建内核")
	}
}
