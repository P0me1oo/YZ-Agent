package xray

import (
	"strconv"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
)

// 三类流量都累计到内核对象上，Start、Stop 和配置角色切换不会清空基线。
// 调用方持有 x.mu，当前实例和排空实例的读后清零操作只会结算一次。
func (x *Xray) collectStatsLocked() {
	x.collectInstanceStatsLocked(x.instance, x.nodeConfig)
	for _, previous := range x.retired {
		x.collectInstanceStatsLocked(previous.instance, previous.config)
	}
}

func (x *Xray) collectInstanceStatsLocked(instance *xrayCore.Instance, config *model.NodeSpec) {
	if instance == nil {
		return
	}
	manager, ok := instance.GetFeature(stats.ManagerType()).(stats.Manager)
	if !ok {
		return
	}
	tagToNode := config.RelayNodeIDByTag()
	routeToNode := make(map[int]int)
	if config.IsRelayEntry() {
		for _, child := range config.Relay.Children {
			if child.RouteID > 0 && child.NodeID > 0 {
				routeToNode[child.RouteID] = child.NodeID
			}
		}
	}
	// 已删除用户的存量连接仍可能写入计数，不能仅遍历当前用户列表。
	manager.VisitCounters(func(name string, counter stats.Counter) bool {
		parts := strings.Split(name, ">>>")
		if len(parts) != 4 && len(parts) != 6 {
			return true
		}
		if parts[len(parts)-2] != "traffic" {
			return true
		}
		direction := 0
		switch parts[len(parts)-1] {
		case "uplink":
		case "downlink":
			direction = 1
		default:
			return true
		}
		var target map[int][2]int64
		var id int
		switch parts[0] {
		case "user":
			uid, err := strconv.Atoi(strings.TrimPrefix(parts[1], "user@"))
			if err != nil || uid <= 0 || !strings.HasPrefix(parts[1], "user@") {
				return true
			}
			if len(parts) == 4 {
				target, id = x.cumTraffic, uid
			} else if parts[2] == "relay" {
				routeID, _ := strconv.Atoi(parts[3])
				id = routeToNode[routeID]
				if id <= 0 {
					return true
				}
				if x.cumRelayUserTraffic[uid] == nil {
					x.cumRelayUserTraffic[uid] = make(map[int][2]int64)
				}
				target = x.cumRelayUserTraffic[uid]
			}
		case "outbound":
			if len(parts) == 4 {
				id, target = tagToNode[parts[1]], x.cumRelayTraffic
			}
		}
		if target != nil && id > 0 {
			if delta := counter.Set(0); delta > 0 {
				current := target[id]
				current[direction] += delta
				target[id] = current
			}
		}
		return true
	})
}

func copyTraffic(source map[int][2]int64) map[int][2]int64 {
	if len(source) == 0 {
		return nil
	}
	copy := make(map[int][2]int64, len(source))
	for id, value := range source {
		copy[id] = value
	}
	return copy
}
