// Package autospeed 执行套餐的临时限速，按节点本地采样判定，不依赖面板页面。
package autospeed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"
)

type Rule struct {
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	Direction string `json:"direction"`
	Threshold int    `json:"threshold_mbps"`
	Target    int    `json:"limit_mbps"`
	Duration  int    `json:"duration_seconds"`
	Window    int    `json:"window_seconds"`
	Hits      int    `json:"hits"`
	Hold      int    `json:"hold_seconds"`
}

func (r *Rule) Valid() bool {
	if r == nil || !r.Enabled {
		return false
	}
	if r.Threshold < 1 || r.Threshold > 100000 || r.Target < 1 || r.Target >= r.Threshold || r.Hold < 1 || r.Hold > 86400 {
		return false
	}
	if r.Direction != "upload" && r.Direction != "download" && r.Direction != "max" {
		return false
	}
	return r.Mode == "continuous" && r.Duration >= 3 && r.Duration <= 3600 || r.Mode == "window" && r.Window >= 3 && r.Window <= 3600 && r.Hits >= 1 && r.Hits <= r.Window
}
func Fingerprint(r *Rule) string {
	data, _ := json.Marshal(r)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type Penalty struct {
	Limit int    `json:"limit_mbps"`
	Until int64  `json:"expires_at"`
	Rule  string `json:"rule"`
}
type state struct {
	config  Rule
	rule    string
	since   time.Time
	hits    []time.Time
	penalty Penalty
}
type Manager struct {
	users map[int]*state
	last  time.Time
}

func (m *Manager) Update(rules map[int]*Rule, rates map[int][2]int64, now time.Time) map[int]Penalty {
	return m.UpdateSample(rules, rates, now, now)
}

// 同步名单时只比较规则值；同一规则的摘要在创建、变更时计算一次。
func (m *Manager) syncRules(rules map[int]*Rule) {
	if m.users == nil {
		m.users = make(map[int]*state)
	}
	for id := range m.users {
		if rules[id] == nil {
			delete(m.users, id)
		}
	}
	for id, r := range rules {
		if s := m.users[id]; r != nil && s != nil && s.config == *r {
			continue
		}
		if !r.Valid() {
			delete(m.users, id)
			continue
		}
		m.users[id] = &state{config: *r, rule: Fingerprint(r)}
	}
}

// UpdateSample 用采样时间去重。名单刷新只更新规则和到期状态，不重复消费旧网速。
func (m *Manager) UpdateSample(rules map[int]*Rule, rates map[int][2]int64, sampledAt, now time.Time) map[int]Penalty {
	m.syncRules(rules)
	sample := !sampledAt.IsZero() && !sampledAt.Equal(m.last)
	gap := m.last.IsZero() || sampledAt.Sub(m.last) > 5*time.Second || sampledAt.Before(m.last)
	if sample {
		m.last = sampledAt
	}
	for id, s := range m.users {
		r := s.config
		if s.penalty.Until > now.Unix() {
			continue
		}
		if s.penalty.Until != 0 {
			s.penalty = Penalty{}
			s.since = time.Time{}
			s.hits = nil
			continue
		}
		if !sample {
			continue
		}
		if gap || rates == nil {
			s.since = time.Time{}
			s.hits = nil
		}
		if rates == nil {
			continue
		}
		v := rates[id]
		speed := v[1]
		if r.Direction == "upload" {
			speed = v[0]
		} else if r.Direction == "max" {
			speed = max(v[0], v[1])
		}
		above := float64(speed)*8 > float64(r.Threshold)*1e6
		trigger := false
		if r.Mode == "continuous" {
			if !above {
				s.since = time.Time{}
			} else if s.since.IsZero() {
				s.since = sampledAt
			} else {
				trigger = sampledAt.Sub(s.since) >= time.Duration(r.Duration)*time.Second
			}
		} else {
			first := 0
			for first < len(s.hits) && !s.hits[first].After(sampledAt.Add(-time.Duration(r.Window)*time.Second)) {
				first++
			}
			s.hits = s.hits[first:]
			// 窗口按秒去重，持续模式仍处理每个新样本中的降速。
			if above && (len(s.hits) == 0 || s.hits[len(s.hits)-1].Unix() != sampledAt.Unix()) {
				s.hits = append(s.hits, sampledAt)
			}
			trigger = len(s.hits) >= r.Hits
		}
		if trigger {
			s.penalty = Penalty{r.Target, now.Add(time.Duration(r.Hold) * time.Second).Unix(), s.rule}
			s.since = time.Time{}
			s.hits = nil
		}
	}
	return m.Snapshot(now)
}
func (m *Manager) Snapshot(now time.Time) map[int]Penalty {
	out := map[int]Penalty{}
	for id, s := range m.users {
		if s.penalty.Until > now.Unix() {
			out[id] = s.penalty
		}
	}
	return out
}
func (m *Manager) Restore(saved map[int]Penalty, rules map[int]*Rule, now time.Time) {
	m.syncRules(rules)
	for id, p := range saved {
		s := m.users[id]
		if s != nil && p.Rule == s.rule && p.Limit == s.config.Target && p.Until > now.Unix() && p.Until <= now.Unix()+int64(s.config.Hold) {
			s.penalty = p
		}
	}
}

// Rate 使用真实采样间隔和非负差额，计数器重置后重新建立基线。
func Rate(current, previous int64, elapsed time.Duration) int64 {
	if elapsed <= 0 || elapsed > 5*time.Second || current < previous || previous < 0 {
		return 0
	}
	return int64(math.Min(9007199254740991, float64(current-previous)/elapsed.Seconds()))
}
