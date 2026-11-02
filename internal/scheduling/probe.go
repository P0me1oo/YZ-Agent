// Package scheduling 探测面板指定的内部落地端口，不访问用户凭据或统计用户流量。
package scheduling

import (
	"context"
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Target struct {
	NodeID  int    `json:"node_id"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
}

type Result struct {
	NodeID    int    `json:"node_id"`
	Token     string `json:"token"`
	Healthy   bool   `json:"healthy"`
	RTTMs     int64  `json:"rtt_ms"`
	CheckedAt int64  `json:"checked_at"`
}

type state struct {
	Result
	good, bad int
}

type Monitor struct {
	mu      sync.Mutex
	targets map[int]Target
	results map[int]state
	wake    chan struct{}
}

func New() *Monitor {
	return &Monitor{targets: make(map[int]Target), results: make(map[int]state), wake: make(chan struct{}, 1)}
}

// Update 保留目标未变时的防抖状态；地址变化后不复用旧测量。
func (m *Monitor) Update(targets []Target) {
	if m == nil {
		return
	}
	next := make(map[int]Target)
	if len(targets) <= 256 {
		for _, target := range targets {
			token, err := hex.DecodeString(target.Token)
			if target.NodeID <= 0 || target.Port < 1 || target.Port > 65535 || len(target.Address) == 0 || len(target.Address) > 253 ||
				strings.ContainsAny(target.Address, "/\\\n\r\t @?#") || err != nil || len(token) != 32 {
				continue
			}
			next[target.NodeID] = target
		}
	}
	m.mu.Lock()
	changed := len(next) != len(m.targets)
	for id, target := range next {
		if m.targets[id] != target {
			changed = true
			delete(m.results, id)
		}
	}
	for id := range m.results {
		if _, ok := next[id]; !ok {
			delete(m.results, id)
		}
	}
	m.targets = next
	m.mu.Unlock()
	if changed {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probe(ctx)
		case <-m.wake:
			m.probe(ctx)
		}
	}
}

func (m *Monitor) probe(ctx context.Context) {
	m.mu.Lock()
	targets := make([]Target, 0, len(m.targets))
	for _, target := range m.targets {
		targets = append(targets, target)
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	var workers sync.WaitGroup
	jobs := make(chan Target)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range jobs {
				started := time.Now()
				attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
				conn, err := (&net.Dialer{}).DialContext(attempt, "tcp", net.JoinHostPort(target.Address, strconv.Itoa(target.Port)))
				cancel()
				if conn != nil {
					conn.Close()
				}
				if ctx.Err() != nil {
					continue
				}
				m.record(target, err == nil, time.Since(started).Milliseconds(), time.Now().Unix())
			}
		}()
	}
	for _, target := range targets {
		select {
		case jobs <- target:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func (m *Monitor) record(target Target, ok bool, rtt, checked int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.targets[target.NodeID] != target {
		return
	}
	previous, exists := m.results[target.NodeID]
	if exists && previous.CheckedAt >= checked {
		return
	}
	if !exists {
		previous.Healthy = ok
	}
	previous.NodeID, previous.Token, previous.CheckedAt = target.NodeID, target.Token, checked
	if ok {
		previous.good++
		previous.bad = 0
		previous.RTTMs = rtt
	} else {
		previous.bad++
		previous.good = 0
	}
	if previous.good >= 2 {
		previous.Healthy = true
		previous.good = 2
	}
	if previous.bad >= 2 {
		previous.Healthy = false
		previous.bad = 2
	}
	m.results[target.NodeID] = previous
}

func (m *Monitor) Snapshot() []Result {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make([]Result, 0, len(m.results))
	for _, value := range m.results {
		results = append(results, value.Result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].NodeID < results[j].NodeID })
	return results
}
