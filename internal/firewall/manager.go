package firewall

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/installroot"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// Controller 由所有节点共享；实现只保留计算后的端口计划，不持有节点凭据。
type Controller interface {
	Apply(context.Context, string, *model.NodeSpec, string) error
	Release(context.Context, string) error
}

type reconciler interface {
	Apply(context.Context, []Rule, []Rule) error
	Close() error
}

type sourceReconciler interface {
	ApplySources(context.Context, map[string]Plan) error
}

type Manager struct {
	mu        sync.Mutex
	lifecycle sync.Mutex
	ctx       context.Context
	shutdown  context.CancelFunc
	closing   bool
	plans     map[string]Plan
	revisions map[string]uint64
	revision  uint64
	backend   reconciler
	enabled   bool
	linux     bool
	closed    bool
	cancel    context.CancelFunc
	done      chan struct{}
	lastErr   error
	confirm   map[string]Plan
	discovery map[string]bool
	expected  map[string]bool
}

func New(cfg config.FirewallConfig, configPath string) (*Manager, error) {
	if err := config.ValidateFirewall(cfg); err != nil {
		return nil, err
	}
	m := &Manager{plans: map[string]Plan{}, enabled: cfg.IsEnabled(), linux: runtime.GOOS == "linux"}
	if !m.linux {
		return m, nil
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("确定防火墙实例标识: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(installroot.CanonicalConfig(abs))))[:12]
	backend, err := newSystemBackend(cfg, scope, systemCommands{})
	if err != nil {
		return nil, err
	}
	m.backend = backend
	// 开启管理时等节点发现与来源校验完成后再回收，避免升级时先删除未知来源的旧规则。
	if m.enabled {
		backend.recovering = true
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := backend.Apply(ctx, nil, nil); err != nil {
		backend.Close()
		return nil, fmt.Errorf("清理遗留防火墙规则: %w", err)
	}
	if err := backend.ApplySources(ctx, nil); err != nil {
		backend.Close()
		return nil, fmt.Errorf("清理遗留来源规则: %w", err)
	}
	return m, nil
}

// Start 定期检查规则，恢复防火墙重载或外部删除造成的缺失，并重试清理失败。
func (m *Manager) Start(parent context.Context) {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	if m.closing {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != nil || m.closed || m.backend == nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel, m.done = cancel, make(chan struct{})
	if m.ctx == nil {
		m.ctx, m.shutdown = context.WithCancel(context.Background())
	}
	go func() {
		defer close(m.done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.mu.Lock()
				err := m.reconcileWithProvision(ctx, "", 0)
				m.mu.Unlock()
				if err != nil && ctx.Err() == nil {
					nlog.Core().Warn("防火墙规则同步失败，将自动重试", "error", err)
				}
			}
		}
	}()
}

func (m *Manager) Apply(ctx context.Context, owner string, node *model.NodeSpec, kernelType string) error {
	return m.apply(ctx, owner, node, kernelType, true)
}

// Prepare 只建立来源保护；确认核心成功监听后才接管端口放行。
func (m *Manager) Prepare(ctx context.Context, owner string, node *model.NodeSpec, kernelType string) error {
	return m.apply(ctx, owner, node, kernelType, false)
}

func (m *Manager) apply(ctx context.Context, owner string, node *model.NodeSpec, kernelType string, ready bool) error {
	if m == nil {
		return nil
	}
	if !node.ManagesFirewall() {
		return m.Release(ctx, owner)
	}
	ctx, finish := m.operationContext(ctx)
	defer finish()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || ctx.Err() != nil {
		return fmt.Errorf("防火墙管理器已停止")
	}
	if !m.enabled {
		if node != nil && (node.SourcePolicy.Enabled() || node.HasRelayFirewall()) {
			return fmt.Errorf("来源限制已开启，但系统防火墙管理已关闭")
		}
		return nil
	}
	plan, err := PlanForNode(node, kernelType)
	if backend, ok := m.backend.(*systemBackend); ok {
		var familyErr error
		plan, familyErr = backend.effectivePlan(plan)
		if familyErr != nil {
			return familyErr
		}
	}
	plan.Ready = ready
	if err == nil && plan.Restricted && (!m.linux || m.backend == nil) {
		return fmt.Errorf("来源限制需要具备防火墙管理权限的 Linux 环境")
	}
	if m.expected != nil {
		m.expected[owner] = true
	}
	if err != nil {
		var confirmation *ConfirmationRequired
		if errors.As(err, &confirmation) {
			m.confirmPlan(owner, plan)
			m.updateProtection()
		}
		return err
	}
	if !m.linux && len(plan.Redirects) > 0 {
		return fmt.Errorf("自动端口跳跃转发仅支持 Linux")
	}
	candidate := make(map[string]Plan, len(m.plans)+1)
	for key, value := range m.plans {
		candidate[key] = value
	}
	candidate[owner] = plan
	claims := make(map[string]Plan, len(candidate)+len(m.confirm))
	for key, value := range candidate {
		claims[key] = value
	}
	for key, held := range m.confirm {
		if key != owner {
			// 同一节点的旧监听和待核对监听属于同一个声明，不能互相误报冲突。
			if old, exists := candidate[key]; exists {
				held.Listeners = append(append([]Rule{}, held.Listeners...), old.Listeners...)
				held.Redirects = append(append([]Rule{}, held.Redirects...), old.Redirects...)
				held.Restricted = held.Restricted || old.Restricted
			}
			claims[key] = held
		}
	}
	if _, _, err := combinePlans(claims); err != nil {
		m.confirmPlan(owner, plan)
		m.updateProtection()
		return err
	}
	previous := m.plans
	m.plans = candidate
	delete(m.confirm, owner)
	if m.revisions == nil {
		m.revisions = make(map[string]uint64)
	}
	m.revision++
	m.revisions[owner] = m.revision
	appliedRevision := m.revision
	// 来源已明确的计划仍参与下一轮核对。后端在写入前检查冲突并保留旧规则，
	// 不能把暂等其他节点发现的计划也标成来源未知，否则相邻端口会互相等待。
	err = m.reconcileWithProvision(ctx, owner, appliedRevision)
	if err != nil && plan.RelayRestricted && m.revisions[owner] == appliedRevision {
		if old, exists := previous[owner]; exists {
			m.plans[owner] = old
		} else {
			delete(m.plans, owner)
		}
		m.confirmPlan(owner, plan)
		m.updateProtection()
	}
	return err
}

// 调用方持有管理锁。安装依赖期间允许其他节点变更计划，返回后重新计算当前计划。
func (m *Manager) reconcileWithProvision(ctx context.Context, owner string, revision uint64) error {
	if backend, ok := m.backend.(*systemBackend); ok {
		provision, err := backend.planSourceTools(ctx, m.plans)
		if err != nil {
			m.lastErr = err
			return err
		}
		if provision != nil {
			m.mu.Unlock()
			err = provision(ctx)
			m.mu.Lock()
			if m.closed || ctx.Err() != nil {
				return context.Canceled
			}
			if revision != 0 && m.revisions[owner] != revision {
				return errors.New("防火墙计划已被替换或撤回")
			}
			if err != nil {
				m.lastErr = err
				return err
			}
		}
	}
	return m.reconcile(ctx)
}

func (m *Manager) Release(ctx context.Context, owner string) error {
	if m == nil {
		return nil
	}
	ctx, finish := m.operationContext(ctx)
	defer finish()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	_, wasPending := m.confirm[owner]
	_, hadPlan := m.plans[owner]
	delete(m.confirm, owner)
	if m.expected != nil {
		m.expected[owner] = true
	}
	delete(m.plans, owner)
	delete(m.revisions, owner)
	m.updateProtection()
	if backend, ok := m.backend.(*systemBackend); ok &&
		(len(backend.state.Owned) > 0 || backend.state.SourceDriver != "" || backend.state.Redirect != "") {
		if backend.recovering {
			return pending("正在确认其他节点的遗留规则，暂未完成防火墙撤回")
		}
		// 重启后节点可能尚未建立运行计划，但显式关闭仍须清理本实例遗留规则。
		return m.reconcile(ctx)
	}
	if !hadPlan && m.lastErr == nil && !wasPending {
		return nil
	}
	return m.reconcile(ctx)
}

func (m *Manager) reconcile(parent context.Context) error {
	if m.backend == nil {
		return nil
	}
	m.updateProtection()
	_, _, err := combinePlans(m.plans)
	if err == nil {
		active := make(map[string]Plan)
		for owner, plan := range m.plans {
			if plan.Ready {
				active[owner] = plan
			}
		}
		allows, redirects, _ := combinePlans(active)
		if backend, ok := m.backend.(*systemBackend); ok {
			backend.plans = active
		}
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		previousSources := []sourcePlan{}
		if backend, ok := m.backend.(*systemBackend); ok {
			previousSources = append(previousSources, backend.state.SourcePlans...)
		}
		if source, ok := m.backend.(sourceReconciler); ok {
			err = source.ApplySources(ctx, m.plans)
		} else {
			for _, plan := range m.plans {
				if plan.Restricted {
					err = fmt.Errorf("防火墙后端不支持来源过滤")
					break
				}
			}
		}
		if err == nil {
			err = m.backend.Apply(ctx, allows, redirects)
			if err != nil {
				if backend, ok := m.backend.(*systemBackend); ok {
					restore := map[string]Plan{}
					for _, old := range previousSources {
						restore[old.Owner] = Plan{Restricted: true, Listeners: old.Listeners, Blocked: old.Blocked}
					}
					restoreCtx, stop := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
					restoreErr := backend.ApplySources(restoreCtx, restore)
					stop()
					if restoreErr != nil {
						err = fmt.Errorf("%w；恢复原来源规则失败: %v", err, restoreErr)
					}
				}
			}
		}
	}
	m.lastErr = err
	return err
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.lifecycle.Lock()
	m.closing = true
	if m.shutdown != nil {
		m.shutdown()
	}
	if m.cancel != nil {
		m.cancel()
	}
	done := m.done
	m.lifecycle.Unlock()
	if done != nil {
		<-done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.plans = map[string]Plan{}
	m.revisions = nil
	m.confirm, m.discovery, m.expected = nil, nil, nil
	err := m.reconcile(context.Background())
	m.closed = true
	if m.backend != nil {
		if closeErr := m.backend.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func (m *Manager) operationContext(parent context.Context) (context.Context, func()) {
	m.lifecycle.Lock()
	if m.ctx == nil {
		m.ctx, m.shutdown = context.WithCancel(context.Background())
	}
	ctx, cancel := context.WithCancel(parent)
	if m.closing {
		cancel()
	}
	stop := context.AfterFunc(m.ctx, cancel)
	m.lifecycle.Unlock()
	return ctx, func() { stop(); cancel() }
}
