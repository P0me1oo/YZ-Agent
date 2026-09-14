package service

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func (s *Service) SetFirewallController(controller firewall.Controller) { s.firewall = controller }

func (s *Service) applyFirewall(ctx context.Context, node *model.NodeSpec, users []model.UserSpec) bool {
	if s.firewall == nil {
		return true
	}
	if err := s.firewall.Apply(ctx, s.timeConsumer, node, s.cfg.Kernel.Type); err != nil {
		s.failRuntime("应用防火墙规则失败", node, users, err)
		return false
	}
	return true
}

// 停止监听后再清理规则。退出上下文已经取消时仍给清理操作独立的执行时间。
func (s *Service) stopKernel() {
	s.kernel.Stop()
	if s.firewall == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.firewall.Release(ctx, s.timeConsumer); err != nil {
		nlog.Core().Error("节点已停止，防火墙规则清理失败，将重试", "error", err)
	}
}
