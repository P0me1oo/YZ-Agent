package timesync

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

type Status string

const (
	StatusDisabled    Status = "disabled"
	StatusNormal      Status = "normal"
	StatusWarning     Status = "warning"
	StatusError       Status = "error"
	StatusCritical    Status = "critical"
	StatusUnavailable Status = "unavailable"
)

type Snapshot struct {
	Enabled     bool       `json:"enabled"`
	Required    bool       `json:"required"`
	Status      Status     `json:"status"`
	OffsetMS    int64      `json:"offset_ms"`
	Source      string     `json:"source,omitempty"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	Samples     int        `json:"samples,omitempty"`
	Stale       bool       `json:"stale,omitempty"`
}

type sample struct {
	server string
	offset time.Duration
}

type managerState struct {
	offset      time.Duration
	source      string
	lastSuccess time.Time
	lastAttempt time.Time
	lastError   string
	samples     int
}

type clockState struct {
	offset      time.Duration
	lastSuccess time.Time
}

type exchangeFunc func(context.Context, N.Dialer, M.Socksaddr) (*ntp.Response, error)

// Manager 维护仅供协议栈使用的校准时间，不修改操作系统时钟。
type Manager struct {
	enabled        bool
	servers        []string
	interval       time.Duration
	timeout        time.Duration
	staleAfter     time.Duration
	warnOffset     time.Duration
	errorOffset    time.Duration
	criticalOffset time.Duration

	mu            sync.RWMutex
	state         managerState
	consumers     map[string]bool
	lastLoggedKey string

	clock atomic.Pointer[clockState]

	checkMu sync.Mutex
	runMu   sync.Mutex
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	exchange exchangeFunc
}

var defaultManager atomic.Pointer[Manager]

func New(cfg config.TimeSyncConfig) *Manager {
	servers := append([]string(nil), cfg.Servers...)
	if len(servers) == 0 {
		servers = append(servers, config.DefaultTimeSyncServers...)
	}
	interval := durationSeconds(cfg.Interval, config.DefaultTimeSyncInterval)
	timeout := durationSeconds(cfg.Timeout, config.DefaultTimeSyncTimeout)
	return &Manager{
		enabled:        cfg.IsEnabled(),
		servers:        servers,
		interval:       interval,
		timeout:        timeout,
		staleAfter:     3 * interval,
		warnOffset:     durationSeconds(cfg.WarnOffset, config.DefaultTimeSyncWarnOffset),
		errorOffset:    durationSeconds(cfg.ErrorOffset, config.DefaultTimeSyncErrorOffset),
		criticalOffset: durationSeconds(cfg.CriticalOffset, config.DefaultTimeSyncCriticalOffset),
		consumers:      make(map[string]bool),
		exchange:       ntp.Exchange,
	}
}

func durationSeconds(value, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func Default() *Manager {
	if manager := defaultManager.Load(); manager != nil {
		return manager
	}
	return disabledManager
}

func SetDefault(manager *Manager) {
	if manager == nil {
		defaultManager.Store(nil)
		return
	}
	defaultManager.Store(manager)
}

var disabledManager = func() *Manager {
	disabled := false
	return New(config.TimeSyncConfig{Enabled: &disabled})
}()

func (m *Manager) Start(parent context.Context) {
	if m == nil || !m.enabled {
		return
	}
	m.runMu.Lock()
	if m.cancel != nil {
		m.runMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	// 将首次检查也纳入等待组，避免 Stop 在首次检查结束前返回，或与
	// 后台循环的启动形成 WaitGroup Add/Wait 竞态。
	m.wg.Add(1)
	m.runMu.Unlock()

	_, _ = m.Check(ctx)
	if ctx.Err() != nil {
		m.wg.Done()
		return
	}
	go m.loop(ctx)
}

func (m *Manager) loop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.Check(ctx)
		}
	}
}

func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.runMu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}

// TimeFunc 实现 sing/common/ntp.TimeService。校准结果过期时直接使用系统时间。
func (m *Manager) TimeFunc() func() time.Time {
	return func() time.Time {
		if m == nil || !m.enabled {
			return time.Now()
		}
		state := m.clock.Load()
		if state == nil || time.Since(state.lastSuccess) > m.staleAfter {
			return time.Now()
		}
		return time.Now().Add(state.offset)
	}
}

func (m *Manager) Check(ctx context.Context) (Snapshot, error) {
	if m == nil || !m.enabled {
		return m.Snapshot(), nil
	}
	m.checkMu.Lock()
	defer m.checkMu.Unlock()

	attemptedAt := time.Now()
	samples, err := m.query(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return m.Snapshot(), ctx.Err()
		}
		m.mu.Lock()
		m.state.lastAttempt = attemptedAt
		m.state.lastError = err.Error()
		m.mu.Unlock()
		snapshot := m.Snapshot()
		m.logIfNeeded(snapshot)
		return snapshot, err
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i].offset < samples[j].offset })
	selected := samples[len(samples)/2]
	succeededAt := time.Now()
	m.clock.Store(&clockState{offset: selected.offset, lastSuccess: succeededAt})
	m.mu.Lock()
	m.state = managerState{
		offset:      selected.offset,
		source:      selected.server,
		lastSuccess: succeededAt,
		lastAttempt: attemptedAt,
		samples:     len(samples),
	}
	m.mu.Unlock()
	snapshot := m.Snapshot()
	m.logIfNeeded(snapshot)
	return snapshot, nil
}

func (m *Manager) query(ctx context.Context) ([]sample, error) {
	resultCh := make(chan struct {
		sample sample
		err    error
	}, len(m.servers))
	for _, rawServer := range m.servers {
		server := strings.TrimSpace(rawServer)
		go func() {
			offset, err := m.queryServer(ctx, server)
			resultCh <- struct {
				sample sample
				err    error
			}{sample: sample{server: server, offset: offset}, err: err}
		}()
	}

	valid := make([]sample, 0, len(m.servers))
	errorsByServer := make([]string, 0, len(m.servers))
	for range m.servers {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-resultCh:
			if result.err != nil {
				errorsByServer = append(errorsByServer, fmt.Sprintf("%s: %v", result.sample.server, result.err))
				continue
			}
			valid = append(valid, result.sample)
		}
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("all NTP servers failed: %s", strings.Join(errorsByServer, "; "))
	}
	return valid, nil
}

func (m *Manager) queryServer(parent context.Context, server string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(parent, m.timeout)
	defer cancel()

	address := M.ParseSocksaddr(server)
	if address.Port == 0 {
		address.Port = 123
	}
	type result struct {
		response *ntp.Response
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := m.exchange(ctx, N.SystemDialer, address)
		done <- result{response: response, err: err}
	}()

	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return 0, result.err
		}
		if result.response == nil {
			return 0, fmt.Errorf("empty NTP response")
		}
		if err := result.response.Validate(); err != nil {
			return 0, err
		}
		return result.response.ClockOffset, nil
	}
}

func (m *Manager) SetUsage(consumer string, required bool) {
	if m == nil || strings.TrimSpace(consumer) == "" {
		return
	}
	m.mu.Lock()
	wasRequired := m.requiredLocked()
	if required {
		m.consumers[consumer] = true
	} else {
		delete(m.consumers, consumer)
	}
	isRequired := m.requiredLocked()
	if wasRequired != isRequired {
		m.lastLoggedKey = ""
	}
	m.mu.Unlock()
	if isRequired {
		m.logIfNeeded(m.Snapshot())
	}
}

func (m *Manager) requiredLocked() bool {
	for _, required := range m.consumers {
		if required {
			return true
		}
	}
	return false
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{Status: StatusDisabled}
	}
	m.mu.RLock()
	state := m.state
	required := m.requiredLocked()
	m.mu.RUnlock()

	snapshot := Snapshot{
		Enabled:   m.enabled,
		Required:  required,
		OffsetMS:  state.offset.Milliseconds(),
		Source:    state.source,
		LastError: state.lastError,
		Samples:   state.samples,
	}
	if !state.lastAttempt.IsZero() {
		lastAttempt := state.lastAttempt
		snapshot.LastAttempt = &lastAttempt
	}
	if !state.lastSuccess.IsZero() {
		lastSuccess := state.lastSuccess
		snapshot.LastSuccess = &lastSuccess
	}
	if !m.enabled {
		snapshot.Status = StatusDisabled
		return snapshot
	}
	if state.lastSuccess.IsZero() {
		snapshot.Status = StatusUnavailable
		return snapshot
	}
	if time.Since(state.lastSuccess) > m.staleAfter {
		snapshot.Status = StatusUnavailable
		snapshot.Stale = true
		return snapshot
	}
	snapshot.Status = m.classify(state.offset)
	return snapshot
}

func (m *Manager) classify(offset time.Duration) Status {
	if offset < 0 {
		offset = -offset
	}
	switch {
	case offset >= m.criticalOffset:
		return StatusCritical
	case offset >= m.errorOffset:
		return StatusError
	case offset >= m.warnOffset:
		return StatusWarning
	default:
		return StatusNormal
	}
}

func (m *Manager) logIfNeeded(snapshot Snapshot) {
	if !snapshot.Required {
		return
	}
	key := string(snapshot.Status)
	if snapshot.LastError != "" {
		key += "|query-error"
	}
	if snapshot.Stale {
		key += "|stale"
	}
	m.mu.Lock()
	if key == m.lastLoggedKey {
		m.mu.Unlock()
		return
	}
	m.lastLoggedKey = key
	m.mu.Unlock()

	args := []any{"status", snapshot.Status, "offset_ms", snapshot.OffsetMS, "source", snapshot.Source}
	if snapshot.LastError != "" {
		args = append(args, "error", snapshot.LastError)
	}
	switch {
	case snapshot.Status == StatusNormal && snapshot.LastError == "":
		nlog.Core().Info("SS2022 protocol clock calibrated", args...)
	case snapshot.Status == StatusWarning || (snapshot.Status == StatusNormal && snapshot.LastError != ""):
		nlog.Core().Warn("SS2022 protocol clock needs attention", args...)
	default:
		nlog.Core().Error("SS2022 protocol clock is not reliable", args...)
	}
}

var _ ntp.TimeService = (*Manager)(nil)
