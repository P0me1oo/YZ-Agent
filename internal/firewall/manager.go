package firewall

import (
	"context"
	"crypto/sha256"
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

type Manager struct {
	mu      sync.Mutex
	plans   map[string]Plan
	backend reconciler
	enabled bool
	linux   bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
	lastErr error
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
	// 首次启动先回收上次异常退出遗留的规则，随后按实际启动成功的节点重建。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := backend.Apply(ctx, nil, nil); err != nil {
		backend.Close()
		return nil, fmt.Errorf("清理遗留防火墙规则: %w", err)
	}
	return m, nil
}

// Start 定期检查规则，恢复防火墙重载或外部删除造成的缺失，并重试清理失败。
func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done != nil || m.closed || m.backend == nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel, m.done = cancel, make(chan struct{})
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
				err := m.reconcile(ctx)
				m.mu.Unlock()
				if err != nil && ctx.Err() == nil {
					nlog.Core().Warn("防火墙规则同步失败，将自动重试", "error", err)
				}
			}
		}
	}()
}

func (m *Manager) Apply(ctx context.Context, owner string, node *model.NodeSpec, kernelType string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("防火墙管理器已停止")
	}
	if !m.enabled {
		return nil
	}
	plan, err := PlanForNode(node, kernelType)
	if err != nil {
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
	if _, _, err := combinePlans(candidate); err != nil {
		return err
	}
	m.plans = candidate
	return m.reconcile(ctx)
}

func (m *Manager) Release(ctx context.Context, owner string) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	if _, ok := m.plans[owner]; !ok && m.lastErr == nil {
		return nil
	}
	delete(m.plans, owner)
	return m.reconcile(ctx)
}

func (m *Manager) reconcile(parent context.Context) error {
	if m.backend == nil {
		return nil
	}
	allows, redirects, err := combinePlans(m.plans)
	if err == nil {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		err = m.backend.Apply(ctx, allows, redirects)
	}
	m.lastErr = err
	return err
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	done := m.done
	m.mu.Unlock()
	if done != nil {
		<-done
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.plans = map[string]Plan{}
	err := m.reconcile(context.Background())
	m.closed = true
	if m.backend != nil {
		if closeErr := m.backend.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}
