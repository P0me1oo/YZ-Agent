package singbox

import (
	"sync"
	"sync/atomic"
)

type userTraffic struct {
	upload         trafficCounter
	download       trafficCounter
	owner          *trafficTotals
	userID, nodeID int
	kind           int
	retired        atomic.Bool
}

// trafficTotals 的生命周期跟随 Node 内核对象，重建 Box 时只更换连接状态。
// 旧连接持有同一组原子计数器，延迟完成的读写也不会在重启时丢失。
type trafficTotals struct {
	mu         sync.RWMutex
	users      map[int]*userTraffic
	relays     map[int]*userTraffic
	relayUsers map[int]map[int]*userTraffic
}

func newTrafficTotals() *trafficTotals {
	return &trafficTotals{users: make(map[int]*userTraffic), relays: make(map[int]*userTraffic), relayUsers: make(map[int]map[int]*userTraffic)}
}

func (t *trafficTotals) user(id int) *userTraffic {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.users[id] == nil {
		t.users[id] = t.newCounter(0, id, 0)
	}
	return t.users[id]
}

func (t *trafficTotals) snapshot() map[int][2]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[int][2]int64, len(t.users))
	for id, user := range t.users {
		value := [2]int64{user.upload.Load(), user.download.Load()}
		if value[0] > 0 || value[1] > 0 {
			out[id] = value
		}
	}
	return out
}

// 中转计数与套餐计数共享读写事件，但分开累计，重载和停止不清空。
type relayCounters struct {
	node *userTraffic
	user *userTraffic
}

func (c relayCounters) upload(n int64) {
	if c.node != nil {
		c.node.upload.Add(n)
		c.user.upload.Add(n)
	}
}

func (c relayCounters) download(n int64) {
	if c.node != nil {
		c.node.download.Add(n)
		c.user.download.Add(n)
	}
}

func (t *trafficTotals) relayCounters(userID, nodeID int) relayCounters {
	if userID <= 0 || nodeID <= 0 {
		return relayCounters{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.relays[nodeID] == nil {
		t.relays[nodeID] = t.newCounter(1, 0, nodeID)
	}
	if t.relayUsers[userID] == nil {
		t.relayUsers[userID] = make(map[int]*userTraffic)
	}
	if t.relayUsers[userID][nodeID] == nil {
		t.relayUsers[userID][nodeID] = t.newCounter(2, userID, nodeID)
	}
	return relayCounters{node: t.relays[nodeID], user: t.relayUsers[userID][nodeID]}
}

func snapshotTraffic(counters map[int]*userTraffic) map[int][2]int64 {
	out := make(map[int][2]int64, len(counters))
	for id, counter := range counters {
		value := [2]int64{counter.upload.Load(), counter.download.Load()}
		if value[0] > 0 || value[1] > 0 {
			out[id] = value
		}
	}
	return out
}

func (t *trafficTotals) relaySnapshot() map[int][2]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return snapshotTraffic(t.relays)
}

func (t *trafficTotals) relayUserSnapshot() map[int]map[int][2]int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make(map[int]map[int][2]int64, len(t.relayUsers))
	for userID, nodes := range t.relayUsers {
		if values := snapshotTraffic(nodes); len(values) > 0 {
			out[userID] = values
		}
	}
	return out
}

// trafficCounter 的快速路径保持原子加法。已回收对象若有迟到回调，
// 只把回收后的新增值转交当前账本，不复用已经结算的旧累计值。
type trafficCounter struct {
	value  atomic.Int64
	parent *userTraffic
}

func (c *trafficCounter) Load() int64 { return c.value.Load() }
func (c *trafficCounter) Add(delta int64) int64 {
	value := c.value.Add(delta)
	if c.parent != nil && c.parent.retired.Load() {
		c.parent.owner.reactivate(c.parent)
	}
	return value
}

func (t *trafficTotals) newCounter(kind, userID, nodeID int) *userTraffic {
	counter := &userTraffic{owner: t, kind: kind, userID: userID, nodeID: nodeID}
	counter.upload.parent, counter.download.parent = counter, counter
	return counter
}

func (t *trafficTotals) reactivate(old *userTraffic) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !old.retired.Load() {
		return
	}
	up, down := old.upload.value.Swap(0), old.download.value.Swap(0)
	if up == 0 && down == 0 {
		return
	}
	var target map[int]*userTraffic
	id := old.userID
	switch old.kind {
	case 0:
		target = t.users
	case 1:
		target, id = t.relays, old.nodeID
	case 2:
		if t.relayUsers[old.userID] == nil {
			t.relayUsers[old.userID] = make(map[int]*userTraffic)
		}
		target, id = t.relayUsers[old.userID], old.nodeID
	}
	if target[id] == nil {
		target[id] = t.newCounter(old.kind, old.userID, old.nodeID)
	}
	target[id].upload.value.Add(up)
	target[id].download.value.Add(down)
}

// 调用方持有 totals.mu；与回收交错的加法会重新登记新增部分。
func retireCounter(counter *userTraffic, expected [2]int64) bool {
	if counter == nil {
		return false
	}
	counter.retired.Store(true)
	actual := [2]int64{counter.upload.value.Swap(0), counter.download.value.Swap(0)}
	if actual != expected {
		counter.upload.value.Add(actual[0])
		counter.download.value.Add(actual[1])
		counter.retired.Store(false)
		return false
	}
	return true
}
