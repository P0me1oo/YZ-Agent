package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func (s *Service) SetFirewallController(controller firewall.Controller) { s.firewall = controller }

func (s *Service) applyFirewall(ctx context.Context, node *model.NodeSpec, users []model.UserSpec) bool {
	return s.updateFirewall(ctx, node, users, false)
}

func (s *Service) prepareFirewall(ctx context.Context, node *model.NodeSpec, users []model.UserSpec) bool {
	return s.updateFirewall(ctx, node, users, true)
}

func (s *Service) updateFirewall(ctx context.Context, node *model.NodeSpec, users []model.UserSpec, prepare bool) bool {
	if s.firewall == nil {
		if node != nil && node.ManagesFirewall() && node.SourcePolicy.Enabled() {
			s.failRuntime("来源限制无法执行", node, users, errors.New("系统防火墙管理器不可用"))
			s.firewallFailureHash = s.failedRuntimeHash
			s.lastFirewallRetry = time.Now()
			return false
		}
		return true
	}
	if !node.ManagesFirewall() {
		if err := s.firewall.Release(ctx, s.timeConsumer); err != nil {
			s.failRuntime("撤回节点防火墙规则失败", node, users, err)
			s.firewallFailureHash = s.failedRuntimeHash
			s.lastFirewallRetry = time.Now()
			return false
		}
		s.firewallNotice.Store(nil)
		s.firewallFailureHash = ""
		return true
	}
	prepared := node
	if node != nil && node.SourcePolicy.Enabled() {
		var err error
		prepared, err = s.prepareSourcePolicy(ctx, node)
		if err != nil {
			s.failRuntime("准备防火墙来源集合失败", node, users, err)
			return false
		}
	}
	apply := s.firewall.Apply
	if preflight, ok := s.firewall.(interface {
		Prepare(context.Context, string, *model.NodeSpec, string) error
	}); prepare && ok {
		apply = preflight.Prepare
	}
	if err := apply(ctx, s.timeConsumer, prepared, s.cfg.Kernel.Type); err != nil {
		if !node.SourcePolicy.Enabled() && node.IsRelayLanding() && node.Relay.EntryNodeID > 0 {
			message := fmt.Sprintf("落地端口放行失败，保留现有监听并重试：%v", err)
			s.firewallNotice.Store(&message)
			nlog.Core().Warn(message)
			return true
		}
		s.failRuntime("应用防火墙规则失败", node, users, err)
		if node.SourcePolicy.Enabled() {
			s.firewallFailureHash = s.failedRuntimeHash
			s.lastFirewallRetry = time.Now()
		}
		return false
	}
	s.firewallNotice.Store(nil)
	s.firewallFailureHash = ""
	return true
}

// 环境恢复后重试来源过滤或撤回错误，不绕过证书及其他配置错误。
func (s *Service) retrySourceFirewall(ctx context.Context) {
	if s.firewallFailureHash == "" || s.failedRuntimeHash != s.firewallFailureHash || s.kernel.IsRunning() ||
		time.Since(s.lastFirewallRetry) < 30*time.Second || s.lastConfig == nil ||
		(len(s.lastUsers) == 0 && !s.lastConfig.IsRelayLanding() && s.lastConfig.Protocol != "balancer") {
		return
	}
	s.lastFirewallRetry = time.Now()
	s.failedRuntimeHash = ""
	s.startKernel(ctx, s.lastConfig, s.lastUsers)
}

func computeKernelConfigHash(node *model.NodeSpec) string {
	if node == nil || node.Relay == nil {
		return computeConfigHash(node)
	}
	copy := *node
	relay := *node.Relay
	relay.Firewall = nil
	copy.Relay = &relay
	return computeConfigHash(&copy)
}

// 停止监听后再清理规则。退出上下文已经取消时仍给清理操作独立的执行时间。
func (s *Service) stopKernel() {
	s.stopKernelFor(nil)
}

var ErrTrafficProtection = errors.New("machine traffic protection")

func (s *Service) stopKernelFor(cause error) {
	if stopper, ok := s.kernel.(kernel.ImmediateStopper); ok && errors.Is(cause, ErrTrafficProtection) {
		stopper.StopNow()
	} else {
		s.kernel.Stop()
	}
	if s.firewall == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.firewall.Release(ctx, s.timeConsumer); err != nil {
		nlog.Core().Error("节点已停止，防火墙规则清理失败，将重试", "error", err)
	}
}
