package xray

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	appstats "github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/serial"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"
	"google.golang.org/protobuf/types/known/emptypb"
)

// 只替换 Agent 实例的统计模块，在线表及通道仍使用核心原实现。
// 占位配置只存在于内存中的 protobuf 配置，不改变用户配置文件或核心依赖。
func init() {
	common.Must(common.RegisterConfig((*emptypb.Empty)(nil), func(ctx context.Context, _ interface{}) (interface{}, error) { return newTrafficStats(ctx) }))
}

func useTrafficStats(config *xrayCore.Config) {
	stock := serial.ToTypedMessage(&appstats.Config{}).Type
	apps := []*serial.TypedMessage{serial.ToTypedMessage(&emptypb.Empty{})}
	for _, app := range config.App {
		if app.Type != stock {
			apps = append(apps, app)
		}
	}
	config.App = apps
}

type trafficStats struct {
	*appstats.Manager
	mu       sync.RWMutex
	counters map[string]*trafficStatCounter
	users    map[int]map[string]*trafficStatCounter
}

func newTrafficStats(ctx context.Context) (*trafficStats, error) {
	base, err := appstats.NewManager(ctx, &appstats.Config{})
	if err != nil {
		return nil, err
	}
	return &trafficStats{Manager: base, counters: make(map[string]*trafficStatCounter), users: make(map[int]map[string]*trafficStatCounter)}, nil
}

type trafficStatCounter struct {
	value   atomic.Int64
	retired atomic.Bool
	owner   *trafficStats
	name    string
	userID  int
}

func (c *trafficStatCounter) Value() int64 { return c.value.Load() }
func (c *trafficStatCounter) Set(value int64) int64 {
	old := c.value.Swap(value)
	if c.retired.Load() {
		c.owner.reactivate(c)
	}
	return old
}
func (c *trafficStatCounter) Add(value int64) int64 {
	total := c.value.Add(value)
	if c.retired.Load() {
		c.owner.reactivate(c)
	}
	return total
}

func (m *trafficStats) newCounterLocked(name string) *trafficStatCounter {
	counter := &trafficStatCounter{owner: m, name: name}
	if suffix, ok := strings.CutPrefix(name, "user>>>user@"); ok {
		if index := strings.Index(suffix, ">>>"); index >= 0 {
			counter.userID, _ = strconv.Atoi(suffix[:index])
		}
	}
	m.counters[name] = counter
	if counter.userID > 0 {
		if m.users[counter.userID] == nil {
			m.users[counter.userID] = make(map[string]*trafficStatCounter)
		}
		m.users[counter.userID][name] = counter
	}
	return counter
}

func (m *trafficStats) RegisterCounter(name string) (stats.Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counters[name] != nil {
		return nil, fmt.Errorf("counter %q already registered", name)
	}
	return m.newCounterLocked(name), nil
}
func (m *trafficStats) GetOrRegisterCounter(name string) (stats.Counter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if counter := m.counters[name]; counter != nil {
		return counter, nil
	}
	return m.newCounterLocked(name), nil
}
func (m *trafficStats) GetCounter(name string) stats.Counter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if counter := m.counters[name]; counter != nil {
		return counter
	}
	return nil
}
func (m *trafficStats) VisitCounters(visitor func(string, stats.Counter) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, counter := range m.counters {
		if !visitor(name, counter) {
			return
		}
	}
}
func (m *trafficStats) deleteLocked(counter *trafficStatCounter) {
	delete(m.counters, counter.name)
	delete(m.users[counter.userID], counter.name)
	if len(m.users[counter.userID]) == 0 {
		delete(m.users, counter.userID)
	}
}
func (m *trafficStats) UnregisterCounter(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if counter := m.counters[name]; counter != nil {
		counter.retired.Store(true)
		counter.value.Swap(0)
		m.deleteLocked(counter)
	}
	return nil
}

// 原始增量已进入累计账本后才允许回收；与采样交错的新字节会取消本次回收。
func (m *trafficStats) retireUser(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	complete := true
	for _, counter := range m.users[id] {
		counter.retired.Store(true)
		if value := counter.value.Swap(0); value != 0 {
			counter.value.Add(value)
			counter.retired.Store(false)
			complete = false
			continue
		}
		m.deleteLocked(counter)
	}
	return complete
}

// 旧连接持有的已回收引用如果迟到写入，只转交新增量；新一代同名计数器不受影响。
func (m *trafficStats) reactivate(old *trafficStatCounter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !old.retired.Load() {
		return
	}
	value := old.value.Swap(0)
	if value == 0 {
		return
	}
	counter := m.counters[old.name]
	if counter == nil {
		counter = m.newCounterLocked(old.name)
	}
	counter.value.Add(value)
}

var _ stats.Manager = (*trafficStats)(nil)
