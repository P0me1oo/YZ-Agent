package singbox

import "github.com/P0me1oo/YZ-Agent/internal/model"

func (s *SingBox) RetireTraffic(candidates model.TrafficCounters) model.TrafficCounters {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := model.TrafficCounters{Users: make(map[int][2]int64), Relay: make(map[int][2]int64), RelayUsers: make(map[int]map[int][2]int64)}
	keepUsers, keepNodes, busy := make(map[int]bool), make(map[int]bool), make(map[int]bool)
	for _, user := range s.users {
		keepUsers[user.ID] = true
	}
	if s.nodeConfig != nil && s.nodeConfig.Relay != nil {
		for _, child := range s.nodeConfig.Relay.Children {
			keepNodes[child.NodeID] = true
		}
	}
	tracker := s.connTracker
	if tracker != nil {
		tracker.usersMu.Lock()
		defer tracker.usersMu.Unlock()
		for id, stats := range tracker.users {
			stats.mu.RLock()
			busy[id] = stats.connCount > 0 || stats.pendingConns > 0
			stats.mu.RUnlock()
		}
	}
	totals := s.traffic
	if totals == nil {
		return out
	}
	totals.mu.Lock()
	defer totals.mu.Unlock()
	for id, value := range candidates.Users {
		if !keepUsers[id] && !busy[id] && retireCounter(totals.users[id], value) {
			delete(totals.users, id)
			if tracker != nil {
				delete(tracker.users, id)
			}
			out.Users[id] = value
		}
	}
	anyBusy := false
	for _, value := range busy {
		anyBusy = anyBusy || value
	}
	for id, value := range candidates.Relay {
		if !keepNodes[id] && !anyBusy && retireCounter(totals.relays[id], value) {
			delete(totals.relays, id)
			out.Relay[id] = value
		}
	}
	for uid, nodes := range candidates.RelayUsers {
		for id, value := range nodes {
			if (!keepUsers[uid] || !keepNodes[id]) && !busy[uid] && retireCounter(totals.relayUsers[uid][id], value) {
				delete(totals.relayUsers[uid], id)
				if out.RelayUsers[uid] == nil {
					out.RelayUsers[uid] = make(map[int][2]int64)
				}
				out.RelayUsers[uid][id] = value
			}
		}
		if len(totals.relayUsers[uid]) == 0 {
			delete(totals.relayUsers, uid)
		}
	}
	return out
}
