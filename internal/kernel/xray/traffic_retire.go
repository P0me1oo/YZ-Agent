package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/features/stats"
)

func (x *Xray) RetireTraffic(candidates model.TrafficCounters) model.TrafficCounters {
	x.mu.Lock()
	defer x.mu.Unlock()
	// 先结算当前和排空实例的最终增量，变化后的账本不能按旧采样基线删除。
	x.collectStatsLocked()
	out := model.TrafficCounters{Users: make(map[int][2]int64), Relay: make(map[int][2]int64), RelayUsers: make(map[int]map[int][2]int64)}
	keepUsers, keepNodes, busy := make(map[int]bool), make(map[int]bool), make(map[int]bool)
	for _, user := range x.users {
		keepUsers[user.ID] = true
	}
	if x.nodeConfig != nil && x.nodeConfig.Relay != nil {
		for _, child := range x.nodeConfig.Relay.Children {
			keepNodes[child.NodeID] = true
		}
	}
	merge := func(dispatcher *LimitDispatcher) {
		if dispatcher == nil {
			return
		}
		counts, _ := dispatcher.ConnectionSnapshot()
		for id, count := range counts {
			busy[id] = busy[id] || count > 0
		}
	}
	merge(x.limitDispatcher)
	for _, retired := range x.retired {
		merge(retired.dispatcher)
	}
	for id, value := range candidates.Users {
		if current, ok := x.cumTraffic[id]; ok && current == value && !keepUsers[id] && !busy[id] {
			complete := true
			if x.instance != nil {
				if manager, ok := x.instance.GetFeature(stats.ManagerType()).(*trafficStats); ok {
					complete = manager.retireUser(id)
				} else {
					complete = false
				}
			}
			for _, previous := range x.retired {
				if previous.instance == nil {
					continue
				}
				if manager, ok := previous.instance.GetFeature(stats.ManagerType()).(*trafficStats); ok {
					complete = manager.retireUser(id) && complete
				} else {
					complete = false
				}
			}
			if !complete {
				continue
			}
			delete(x.cumTraffic, id)
			out.Users[id] = value
		}
	}
	anyBusy := false
	for _, value := range busy {
		anyBusy = anyBusy || value
	}
	for id, value := range candidates.Relay {
		if current, ok := x.cumRelayTraffic[id]; ok && current == value && !keepNodes[id] && !anyBusy {
			delete(x.cumRelayTraffic, id)
			out.Relay[id] = value
		}
	}
	for uid, nodes := range candidates.RelayUsers {
		for id, value := range nodes {
			if current, ok := x.cumRelayUserTraffic[uid][id]; ok && current == value && (!keepUsers[uid] || !keepNodes[id]) && !busy[uid] {
				delete(x.cumRelayUserTraffic[uid], id)
				if out.RelayUsers[uid] == nil {
					out.RelayUsers[uid] = make(map[int][2]int64)
				}
				out.RelayUsers[uid][id] = value
			}
		}
		if len(x.cumRelayUserTraffic[uid]) == 0 {
			delete(x.cumRelayUserTraffic, uid)
		}
	}
	return out
}
