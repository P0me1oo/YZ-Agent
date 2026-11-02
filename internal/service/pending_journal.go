package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

type pendingJournalRecord struct {
	Sequence     uint64         `json:"sequence"`
	BatchChanged bool           `json:"batch_changed,omitempty"`
	Batch        *pendingBatch  `json:"batch,omitempty"`
	Pending      pendingTraffic `json:"delta"`
}

func diffTraffic(before, after map[int][2]int64) map[int][2]int64 {
	out := make(map[int][2]int64)
	for id, value := range after {
		old := before[id]
		if value != old {
			out[id] = [2]int64{value[0] - old[0], value[1] - old[1]}
		}
	}
	for id, value := range before {
		if _, ok := after[id]; !ok && value != ([2]int64{}) {
			out[id] = [2]int64{-value[0], -value[1]}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func diffPending(before, after pendingTraffic) pendingTraffic {
	delta := pendingTraffic{Traffic: diffTraffic(before.Traffic, after.Traffic), Relay: diffTraffic(before.Relay, after.Relay)}
	for id, values := range after.RelayUser {
		if diff := diffTraffic(before.RelayUser[id], values); len(diff) > 0 {
			if delta.RelayUser == nil {
				delta.RelayUser = make(map[int]map[int][2]int64)
			}
			delta.RelayUser[id] = diff
		}
	}
	for id, values := range before.RelayUser {
		if _, ok := after.RelayUser[id]; !ok {
			if delta.RelayUser == nil {
				delta.RelayUser = make(map[int]map[int][2]int64)
			}
			delta.RelayUser[id] = diffTraffic(values, nil)
		}
	}
	return delta
}

func applyTrafficDelta(base, delta map[int][2]int64) (map[int][2]int64, error) {
	if len(delta) == 0 {
		return base, nil
	}
	if base == nil {
		base = make(map[int][2]int64)
	}
	for id, value := range delta {
		old := base[id]
		next := [2]int64{old[0] + value[0], old[1] + value[1]}
		if next[0] < 0 || next[1] < 0 {
			return nil, fmt.Errorf("invalid pending traffic journal delta")
		}
		if next == ([2]int64{}) {
			delete(base, id)
		} else {
			base[id] = next
		}
	}
	return base, nil
}

func (s *pendingReportStore) replayJournal(file *pendingReportFile) error {
	dir := s.path + ".journal"
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// 原子写入遗留的临时文件从未发布，不参与恢复。
		if strings.HasPrefix(entry.Name(), ".private-write-") {
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("unexpected traffic journal entry")
		}
		data, err := privacy.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read traffic journal: %w", err)
		}
		var record pendingJournalRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("decode traffic journal: %w", err)
		}
		if entry.Name() != fmt.Sprintf("%020d.json", record.Sequence) {
			return fmt.Errorf("invalid traffic journal sequence")
		}
		if record.Sequence <= file.JournalSequence {
			continue
		}
		if record.Sequence != file.JournalSequence+1 {
			return fmt.Errorf("traffic journal sequence gap")
		}
		if record.BatchChanged {
			file.Batch = record.Batch
		}
		if file.Pending.Traffic, err = applyTrafficDelta(file.Pending.Traffic, record.Pending.Traffic); err != nil {
			return err
		}
		if file.Pending.Relay, err = applyTrafficDelta(file.Pending.Relay, record.Pending.Relay); err != nil {
			return err
		}
		for id, values := range record.Pending.RelayUser {
			if file.Pending.RelayUser == nil {
				file.Pending.RelayUser = make(map[int]map[int][2]int64)
			}
			file.Pending.RelayUser[id], err = applyTrafficDelta(file.Pending.RelayUser[id], values)
			if err != nil {
				return err
			}
			if len(file.Pending.RelayUser[id]) == 0 {
				delete(file.Pending.RelayUser, id)
			}
		}
		file.JournalSequence = record.Sequence
	}
	return nil
}
