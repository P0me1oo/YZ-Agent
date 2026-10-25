// Package balancer 实现前置池的连接分配，已有连接不参与重新选择。
package balancer

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
)

type Target struct {
	ID       int    `json:"id"`
	Address  string `json:"address"`
	Priority int    `json:"priority"`
	Online   bool   `json:"online"`
}

// Selector 在当前可用的最高优先级内轮询；数字越小，优先级越高。
type Selector struct {
	mu      sync.Mutex
	targets []Target
	cursors map[int]uint64
}

func NewSelector(targets []Target) (*Selector, error) {
	s := &Selector{cursors: make(map[int]uint64)}
	if err := s.Update(targets); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Selector) Update(targets []Target) error {
	copyTargets := append([]Target(nil), targets...)
	seen := make(map[int]bool)
	addresses := make(map[string]bool)
	for _, target := range copyTargets {
		if target.ID <= 0 || seen[target.ID] || target.Priority < 1 || target.Priority > 65535 {
			return fmt.Errorf("前置编号或优先级无效")
		}
		host, port, err := net.SplitHostPort(target.Address)
		portNumber, portError := strconv.Atoi(port)
		if err != nil || host == "" || portError != nil || portNumber < 1 || portNumber > 65535 || addresses[target.Address] {
			return fmt.Errorf("前置连接地址无效或重复")
		}
		seen[target.ID], addresses[target.Address] = true, true
	}
	sort.Slice(copyTargets, func(i, j int) bool {
		if copyTargets[i].Priority == copyTargets[j].Priority {
			return copyTargets[i].ID < copyTargets[j].ID
		}
		return copyTargets[i].Priority < copyTargets[j].Priority
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = copyTargets
	return nil
}

// Pick 允许建连失败后跳过本次已经尝试的成员，不永久改变心跳状态。
func (s *Selector) Pick(excluded map[int]bool) (Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var eligible []Target
	for _, target := range s.targets {
		if !target.Online || excluded[target.ID] {
			continue
		}
		if len(eligible) > 0 && eligible[0].Priority != target.Priority {
			break
		}
		eligible = append(eligible, target)
	}
	if len(eligible) == 0 {
		return Target{}, false
	}
	priority := eligible[0].Priority
	index := s.cursors[priority] % uint64(len(eligible))
	s.cursors[priority]++
	return eligible[index], true
}
