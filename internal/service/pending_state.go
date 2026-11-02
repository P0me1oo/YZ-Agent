package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
)

func clonePendingState(state pendingReportFile) pendingReportFile {
	state.Pending = clonePendingTraffic(state.Pending)
	if state.Batch != nil {
		batch := *state.Batch
		batch.pendingTraffic = clonePendingTraffic(batch.pendingTraffic)
		batch.LimitEvents = append(batch.LimitEvents[:0:0], batch.LimitEvents...)
		state.Batch = &batch
	}
	return state
}

func clonePendingTraffic(traffic pendingTraffic) pendingTraffic {
	copy := pendingTraffic{Traffic: cloneTraffic(traffic.Traffic), Relay: cloneTraffic(traffic.Relay)}
	if len(traffic.RelayUser) > 0 {
		copy.RelayUser = make(map[int]map[int][2]int64, len(traffic.RelayUser))
		for uid, nodes := range traffic.RelayUser {
			copy.RelayUser[uid] = cloneTraffic(nodes)
		}
	}
	return copy
}

// 每条记录带不同类别和完整身份后计算摘要，再无序合并。
// 只有检查点需要编码完整待上报流量；增量保存不再每秒编码整份 JSON。
func pendingStateHash(state pendingReportFile) ([32]byte, error) {
	batch, err := json.Marshal(state.Batch)
	if err != nil {
		return [32]byte{}, err
	}
	hash := sha256.Sum256(append([]byte(state.Identity+"\x00"), batch...))
	add := func(kind byte, uid, node int, value [2]int64) {
		var row [33]byte
		row[0] = kind
		binary.LittleEndian.PutUint64(row[1:9], uint64(uid))
		binary.LittleEndian.PutUint64(row[9:17], uint64(node))
		binary.LittleEndian.PutUint64(row[17:25], uint64(value[0]))
		binary.LittleEndian.PutUint64(row[25:33], uint64(value[1]))
		digest := sha256.Sum256(row[:])
		for i := range hash {
			hash[i] ^= digest[i]
		}
	}
	for id, value := range state.Pending.Traffic {
		add(1, id, 0, value)
	}
	for id, value := range state.Pending.Relay {
		add(2, 0, id, value)
	}
	for uid, nodes := range state.Pending.RelayUser {
		for id, value := range nodes {
			add(3, uid, id, value)
		}
	}
	return hash, nil
}
