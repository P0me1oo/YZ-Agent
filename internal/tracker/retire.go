package tracker

import "github.com/P0me1oo/YZ-Agent/internal/model"

// RetireConfirmed 与采样、批次移动互斥，尚待上报的条目不能回收。
// 调用方同时持有报告状态锁，确保不存在待重试或在途批次。
func (t *Tracker) RetireConfirmed(retire func(model.TrafficCounters) model.TrafficCounters) {
	t.mu.Lock()
	defer t.mu.Unlock()
	candidates := model.TrafficCounters{Users: make(map[int][2]int64), Relay: make(map[int][2]int64), RelayUsers: make(map[int]map[int][2]int64)}
	for id, value := range t.lastSeen {
		if _, ok := t.pendingTraffic[id]; !ok {
			candidates.Users[id] = value
		}
	}
	for id, value := range t.lastSeenRelay {
		if _, ok := t.pendingRelay[id]; !ok {
			candidates.Relay[id] = value
		}
	}
	for uid, nodes := range t.lastSeenRelayUser {
		for id, value := range nodes {
			if _, ok := t.pendingRelayUser[uid][id]; !ok {
				if candidates.RelayUsers[uid] == nil {
					candidates.RelayUsers[uid] = make(map[int][2]int64)
				}
				candidates.RelayUsers[uid][id] = value
			}
		}
	}
	removed := retire(candidates)
	for id := range removed.Users {
		delete(t.lastSeen, id)
	}
	for id := range removed.Relay {
		delete(t.lastSeenRelay, id)
	}
	for uid, nodes := range removed.RelayUsers {
		for id := range nodes {
			delete(t.lastSeenRelayUser[uid], id)
		}
		if len(t.lastSeenRelayUser[uid]) == 0 {
			delete(t.lastSeenRelayUser, uid)
		}
	}
}
