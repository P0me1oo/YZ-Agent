package singbox

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

func (s *SingBox) validateRelay(nc *model.NodeSpec, users []model.UserSpec) error {
	if nc == nil || nc.Relay == nil {
		return nil
	}
	cfg := s.cfg
	cfg.Type = "singbox"
	if err := model.ValidateNodeSpec(nc, cfg); err != nil {
		return err
	}
	return validateRelayUsers(nc, users)
}

// 先准备新旧身份都能正确选路的规则，再切换认证，最后清理旧规则。
// 任一步失败都恢复旧认证与旧规则；不能恢复时关闭实例，让上层重新启动。
func (s *SingBox) reloadRelayUsersLocked(users []model.UserSpec, next option.Options) error {
	router := service.FromContext[adapter.Router](s.ctx)
	updater, ok := router.(interface {
		UpdateRules([]option.Rule, []option.RuleSet) error
	})
	if !ok {
		return fmt.Errorf("sing-box router does not support relay user updates")
	}
	optionsFor := func(list []model.UserSpec) (option.Options, error) {
		cfg, err := buildConfig(s.cfg, s.nodeConfig, list, s.tls)
		if err != nil {
			return option.Options{}, err
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			return option.Options{}, err
		}
		return singJSON.UnmarshalExtendedContext[option.Options](s.ctx, data)
	}
	previous, err := optionsFor(s.users)
	if err != nil {
		return err
	}
	combined := append([]model.UserSpec{}, s.users...)
	seen := make(map[string]bool, len(combined))
	for _, user := range combined {
		seen[relayUserName(user, 0)] = true
	}
	for _, user := range users {
		if !seen[relayUserName(user, 0)] {
			combined = append(combined, user)
		}
	}
	transition, err := optionsFor(combined)
	if err != nil {
		return err
	}
	if err := updater.UpdateRules(transition.Route.Rules, transition.Route.RuleSet); err != nil {
		return fmt.Errorf("prepare relay user routes: %w", err)
	}
	if s.connTracker != nil {
		s.connTracker.setNodeUsers(s.nodeConfig, combined)
	}
	rollback := func(cause error) error {
		inboundErr := s.updateInboundUsersLocked(previous)
		routeErr := updater.UpdateRules(previous.Route.Rules, previous.Route.RuleSet)
		if s.connTracker != nil {
			s.connTracker.setNodeUsers(s.nodeConfig, s.users)
		}
		if inboundErr != nil || routeErr != nil {
			s.stop()
			return errors.Join(cause, fmt.Errorf("restore relay users: %v; restore routes: %v", inboundErr, routeErr))
		}
		return cause
	}
	if err := s.updateInboundUsersLocked(next); err != nil {
		return rollback(err)
	}
	// 先撤销旧会话的连接权限，再删除旧选路规则，避免更新间隙退回默认出站。
	if s.connTracker != nil {
		s.connTracker.setNodeUsers(s.nodeConfig, users)
	}
	if err := updater.UpdateRules(next.Route.Rules, next.Route.RuleSet); err != nil {
		return rollback(fmt.Errorf("finish relay user routes: %w", err))
	}
	return nil
}
