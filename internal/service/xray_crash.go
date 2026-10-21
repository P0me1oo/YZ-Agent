package service

import (
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// 核心生命周期故障独立于普通连接错误；未恢复的同次故障只登记一次。
func (s *Service) reportXrayCrash(operation string, err error) {
	if s.cfg == nil || s.kernel.Name() != "xray" || s.xrayCrashReported {
		return
	}
	switch operation {
	case "启动内核失败", "重载内核失败", "内核意外停止", "更新用户失败", "新增用户失败", "删除用户失败":
	default:
		return
	}
	s.xrayCrashReported = true
	nlog.Publish(s.cfg.LogScope(), nlog.Event{
		NodeID: s.cfg.Panel.NodeID, Source: "xray", Level: "error", Event: "xray.crash",
		Message: fmt.Sprintf("%s，内核已停止：%v", operation, err),
	})
}
