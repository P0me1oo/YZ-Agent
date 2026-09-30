package firewall

import "fmt"

// BeginDiscovery 由进程在启动任何节点前调用，防止一个快节点提前清理慢节点的旧规则。
func (m *Manager) BeginDiscovery(instances []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.discovery = make(map[string]bool, len(instances))
	m.expected = map[string]bool{}
	for _, instance := range instances {
		m.discovery[instance] = true
	}
	m.updateProtection()
}

// ExpectNodes 只声明已成功发现的节点；发现失败时保留恢复保护，等待下一次成功发现。
func ExpectNodes(controller Controller, instance string, ids []int) {
	if m, ok := controller.(*Manager); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.expected == nil {
			m.expected = map[string]bool{}
		}
		for _, id := range ids {
			owner := fmt.Sprintf("%s/node/%d", instance, id)
			if _, exists := m.expected[owner]; !exists {
				m.expected[owner] = false
			}
		}
		delete(m.discovery, instance)
		m.updateProtection()
	}
}

func (m *Manager) confirmPlan(owner string, plan Plan) {
	if m.confirm == nil {
		m.confirm = map[string]Plan{}
	}
	m.confirm[owner] = plan
}

func (m *Manager) updateProtection() {
	b, ok := m.backend.(*systemBackend)
	if !ok {
		return
	}
	b.recovering = len(m.discovery) > 0
	for _, seen := range m.expected {
		b.recovering = b.recovering || !seen
	}
	b.protected = nil
	for _, plan := range m.confirm {
		b.protected = append(b.protected, plan.Listeners...)
	}
}

func (b *systemBackend) keepOwned(rule Rule) bool {
	if b.recovering {
		return true
	}
	for _, scope := range b.protected {
		if overlaps(scope, rule) {
			return true
		}
	}
	return false
}
