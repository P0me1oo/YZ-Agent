package model

// TrafficCounters 表示已采样并已获确认的累计基线，供内核安全回收历史账本。
type TrafficCounters struct {
	Users      map[int][2]int64
	Relay      map[int][2]int64
	RelayUsers map[int]map[int][2]int64
}
