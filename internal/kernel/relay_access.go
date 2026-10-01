package kernel

import "sync"

// RelayAccess 将线路准入和连接登记放在同一把锁内，撤权同时关闭已登记的 TCP/UDP。
// nil 名单表示普通节点；非 nil 的空名单拒绝所有线路。
type RelayAccess struct {
	mu          sync.Mutex
	allowed     map[string]bool
	connections map[string]relayConnection
}

type relayConnection struct {
	identity string
	close    func()
}

func (a *RelayAccess) Replace(allowed map[string]bool) {
	a.mu.Lock()
	a.allowed = allowed
	var closeConnections []func()
	for id, conn := range a.connections {
		if allowed != nil && !allowed[conn.identity] {
			closeConnections = append(closeConnections, conn.close)
			delete(a.connections, id)
		}
	}
	a.mu.Unlock()
	// 关闭回调会注销连接，不能在持锁时调用。
	for _, closeConnection := range closeConnections {
		closeConnection()
	}
}

func (a *RelayAccess) Allows(identity string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allowed == nil || a.allowed[identity]
}

func (a *RelayAccess) Register(id, identity string, closeConnection func()) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.allowed == nil {
		return true
	}
	if !a.allowed[identity] {
		return false
	}
	if a.connections == nil {
		a.connections = make(map[string]relayConnection)
	}
	a.connections[id] = relayConnection{identity, closeConnection}
	return true
}

func (a *RelayAccess) Remove(id string) {
	a.mu.Lock()
	delete(a.connections, id)
	a.mu.Unlock()
}
