package tracker

import "sort"

// ProcessRelay 根据入口内部出站的累计计数计算逻辑节点增量。
// 该数据只用于落地线路运营统计，与用户流量分开上报，不能计入套餐扣除。
func (t *Tracker) ProcessRelay(cumRelay map[int][2]int64) {
	if len(cumRelay) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.lastSeenRelay == nil {
		t.lastSeenRelay = make(map[int][2]int64, len(cumRelay))
	}
	if t.pendingRelay == nil {
		t.pendingRelay = make(map[int][2]int64, len(cumRelay))
	}

	for nodeID, cum := range cumRelay {
		prev := t.lastSeenRelay[nodeID]
		deltaUp := cum[0] - prev[0]
		deltaDown := cum[1] - prev[1]

		// Guard against counter reset (kernel restart).
		if deltaUp < 0 {
			deltaUp = cum[0]
		}
		if deltaDown < 0 {
			deltaDown = cum[1]
		}

		t.lastSeenRelay[nodeID] = cum

		if deltaUp > 0 || deltaDown > 0 {
			cur := t.pendingRelay[nodeID]
			cur[0] += deltaUp
			cur[1] += deltaDown
			t.pendingRelay[nodeID] = cur
		}
	}
}

// FlushRelayTraffic 取出并清空已累计的逻辑节点流量。
func (t *Tracker) FlushRelayTraffic() map[int][2]int64 {
	t.mu.Lock()
	data := t.pendingRelay
	t.pendingRelay = make(map[int][2]int64, len(data))
	t.mu.Unlock()
	if len(data) == 0 {
		return nil
	}
	return data
}

// RestoreRelayTraffic 在上报失败后把逻辑节点流量放回待上报缓冲。
func (t *Tracker) RestoreRelayTraffic(data map[int][2]int64) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	if t.pendingRelay == nil {
		t.pendingRelay = make(map[int][2]int64, len(data))
	}
	for nodeID, d := range data {
		cur := t.pendingRelay[nodeID]
		cur[0] += d[0]
		cur[1] += d[1]
		t.pendingRelay[nodeID] = cur
	}
	t.mu.Unlock()
}

// ProcessRelayUser 根据累计中转计数计算用户-逻辑节点增量，只用于归属统计，不能计入套餐扣除。
func (t *Tracker) ProcessRelayUser(cum map[int]map[int][2]int64) {
	if len(cum) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	for uid, nodes := range cum {
		if t.lastSeenRelayUser[uid] == nil {
			t.lastSeenRelayUser[uid] = make(map[int][2]int64, len(nodes))
		}
		if t.pendingRelayUser[uid] == nil {
			t.pendingRelayUser[uid] = make(map[int][2]int64, len(nodes))
		}

		for nodeID, current := range nodes {
			previous := t.lastSeenRelayUser[uid][nodeID]
			deltaUp := current[0] - previous[0]
			deltaDown := current[1] - previous[1]
			if deltaUp < 0 {
				deltaUp = current[0]
			}
			if deltaDown < 0 {
				deltaDown = current[1]
			}

			t.lastSeenRelayUser[uid][nodeID] = current
			if deltaUp > 0 || deltaDown > 0 {
				pending := t.pendingRelayUser[uid][nodeID]
				pending[0] += deltaUp
				pending[1] += deltaDown
				t.pendingRelayUser[uid][nodeID] = pending
			}
		}
	}
}

// FlushRelayUserTraffic 取出并清空用户-逻辑节点流量增量。
func (t *Tracker) FlushRelayUserTraffic() map[int]map[int][2]int64 {
	t.mu.Lock()
	data := t.pendingRelayUser
	t.pendingRelayUser = make(map[int]map[int][2]int64, len(data))
	t.mu.Unlock()
	if len(data) == 0 {
		return nil
	}
	return data
}

// RestoreRelayUserTraffic 在上报失败后把用户-逻辑节点流量放回待上报缓冲。
func (t *Tracker) RestoreRelayUserTraffic(data map[int]map[int][2]int64) {
	if len(data) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for uid, nodes := range data {
		if t.pendingRelayUser[uid] == nil {
			t.pendingRelayUser[uid] = make(map[int][2]int64, len(nodes))
		}
		for nodeID, delta := range nodes {
			pending := t.pendingRelayUser[uid][nodeID]
			pending[0] += delta[0]
			pending[1] += delta[1]
			t.pendingRelayUser[uid][nodeID] = pending
		}
	}
}

// ProcessRelayAlive 保存中转入口按实际出网节点拆分的在线来源快照。
// 内核每次返回新副本，nil 表示当前没有中转入口连接。
func (t *Tracker) ProcessRelayAlive(alive map[int]map[int]map[string]bool) {
	t.relayAlive.Store(&alive)
}

// RelayUserAlive 返回最近一次快照：用户 ID => 出网节点 ID => 排序后的来源 IP。
// 节点 0 表示入口直连；保留全部来源，与在线人数口径一致，由面板和插件按需筛选公网地址。
func (t *Tracker) RelayUserAlive() map[int]map[int][]string {
	ptr := t.relayAlive.Load()
	if ptr == nil || len(*ptr) == 0 {
		return nil
	}
	out := make(map[int]map[int][]string, len(*ptr))
	for uid, nodes := range *ptr {
		for nodeID, ips := range nodes {
			if len(ips) == 0 {
				continue
			}
			list := make([]string, 0, len(ips))
			for ip := range ips {
				list = append(list, ip)
			}
			sort.Strings(list)
			if out[uid] == nil {
				out[uid] = make(map[int][]string, len(nodes))
			}
			out[uid][nodeID] = list
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
