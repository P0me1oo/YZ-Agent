package service

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/autospeed"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

type autoSpeedState struct {
	rules      map[int]*autospeed.Rule
	readFailed bool
	manager    autospeed.Manager
	loaded     bool
	active     map[int]autospeed.Penalty
	error      string
}

func (s *Service) autoSpeedPath() string {
	if s.pendingStore == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(s.pendingStore.path), "auto-speed-"+filepath.Base(s.pendingStore.path))
}
func (s *Service) updateAutoSpeed(now time.Time) {
	if s.autoSpeed.rules == nil {
		s.autoSpeed.rules = make(map[int]*autospeed.Rule)
		for _, u := range s.lastUsers {
			if u.AutoSpeedLimit.Valid() {
				s.autoSpeed.rules[u.ID] = u.AutoSpeedLimit
			}
		}
	}
	rules := s.autoSpeed.rules
	if !s.autoSpeed.loaded {
		s.autoSpeed.loaded = true
		if path := s.autoSpeedPath(); path != "" {
			if data, err := privacy.ReadFile(path); err == nil {
				var saved map[int]autospeed.Penalty
				if err := json.Unmarshal(data, &saved); err == nil {
					s.autoSpeed.manager.Restore(saved, rules, now)
				} else {
					s.failures.Report(s.logger(), "临时限速状态解析失败，保留原文件", err)
					s.autoSpeed.readFailed = true
					s.autoSpeed.error = "临时限速状态损坏，已保留原文件"
				}
			} else if !os.IsNotExist(err) {
				s.failures.Report(s.logger(), "临时限速状态读取失败", err)
				s.autoSpeed.error = "读取临时限速状态失败"
				s.autoSpeed.readFailed = true
			}
		}
	}
	for _, id := range s.userSpeed.reset {
		s.autoSpeed.manager.ResetSample(id)
	}
	s.userSpeed.reset = s.userSpeed.reset[:0]
	active := s.autoSpeed.manager.UpdateSampleWithGap(rules, s.userSpeed.rates, s.userSpeed.at, now, s.userSpeed.maxGap)
	if s.autoSpeed.active != nil && maps.Equal(active, s.autoSpeed.active) && s.autoSpeed.error == "" {
		return
	}
	limits := map[int]int{}
	for id, p := range active {
		limits[id] = p.Limit
	}
	s.speedTracker.SetTemporary(limits)
	if refresher, ok := s.kernel.(kernel.SpeedLimitRefresher); ok {
		refresher.RefreshSpeedLimits()
	}
	s.autoSpeed.active = active
	if path := s.autoSpeedPath(); path != "" && !s.autoSpeed.readFailed {
		err := os.MkdirAll(filepath.Dir(path), 0700)
		if err == nil {
			var data []byte
			data, err = json.Marshal(active)
			if err == nil {
				err = privacy.WriteFile(path, data, 0600)
			}
		}
		s.failures.Report(s.logger(), "临时限速状态保存失败", err)
		if err != nil {
			s.autoSpeed.error = "保存临时限速状态失败"
		} else {
			s.autoSpeed.error = ""
		}
	}
}
