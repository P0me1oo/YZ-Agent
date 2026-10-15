// Package machinetraffic 对宿主机外网网卡计量，不读取代理用户统计。
package machinetraffic

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
	_ "time/tzdata"
)

const MaxBytes uint64 = 9007199254740991

type Policy struct {
	Limit       uint64   `json:"limit"`
	Mode        string   `json:"mode"`
	ResetDay    int      `json:"reset_day"`
	Timezone    string   `json:"timezone"`
	Interfaces  []string `json:"interfaces"`
	Revision    uint64   `json:"revision"`
	Epoch       uint64   `json:"epoch"`
	PeriodStart int64    `json:"period_start"`
	PeriodEnd   int64    `json:"period_end"`
	RX          uint64   `json:"rx"`
	TX          uint64   `json:"tx"`
	Baseline    uint64   `json:"baseline"`
	Sequence    uint64   `json:"sequence"`
	Initialized bool     `json:"initialized"`
	Active      bool     `json:"active"`
}

type Sample struct {
	Epoch       uint64   `json:"epoch"`
	PeriodStart int64    `json:"period_start"`
	Sequence    uint64   `json:"sequence"`
	RX          uint64   `json:"rx"`
	TX          uint64   `json:"tx"`
	Initialized bool     `json:"initialized"`
	Error       string   `json:"error,omitempty"`
	Interfaces  []string `json:"interfaces"`
}

type Counter struct {
	Identity string
	RX       uint64
	TX       uint64
}

type Reading struct {
	Boot     string
	Counters map[string]Counter
}

type Reader func([]string) (Reading, error)

type diskState struct {
	Version  int
	Policy   Policy
	Sample   Sample
	Previous Reading
}

// Meter 的每次采样先持久化再允许代理运行；写盘失败立即进入保护。
type Meter struct {
	mu          sync.Mutex
	path        string
	reader      Reader
	state       diskState
	loaded      bool
	lost        bool
	writeFailed bool
}

func New(path string, reader Reader) *Meter {
	if reader == nil {
		reader = Read
	}
	m := &Meter{path: path, reader: reader}
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &m.state)
		if err == nil && m.state.Version == 1 && m.state.Policy.Epoch > 0 {
			m.loaded = true
			m.lost = m.state.Sample.Error == "state_lost"
		} else {
			m.lost = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.lost = true
	}
	return m
}

func (m *Meter) Apply(p Policy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.Epoch == 0 || p.Revision == 0 || p.ResetDay < 1 || p.ResetDay > 31 || p.PeriodEnd <= p.PeriodStart || (p.Mode != "outbound" && p.Mode != "both") {
		return errors.New("invalid machine traffic policy")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return errors.New("invalid machine traffic timezone")
	}
	old := m.state.Policy
	if m.loaded && p.Revision < old.Revision {
		return nil
	}
	if !m.loaded || p.Epoch != old.Epoch {
		// 面板已收到过计量而本地状态丢失时，不能用新的零点绕过额度。
		m.lost = p.Initialized
		m.state = diskState{Version: 1, Policy: p, Sample: Sample{
			Epoch: p.Epoch, PeriodStart: p.PeriodStart, RX: p.RX, TX: p.TX, Sequence: p.Sequence,
		}}
		m.loaded = true
	} else {
		// 面板滞后的旧周期不能覆盖 Node 离线时已经完成的新周期。
		if p.PeriodStart < old.PeriodStart {
			p.PeriodStart, p.PeriodEnd, p.Baseline = old.PeriodStart, old.PeriodEnd, old.Baseline
			p.RX, p.TX, p.Sequence = 0, 0, 0
		} else if p.PeriodStart > old.PeriodStart {
			m.state.Sample = Sample{Epoch: p.Epoch, PeriodStart: p.PeriodStart}
			if m.lost {
				m.lost = false
				m.state.Previous = Reading{}
			}
		}
		if !slices.Equal(p.Interfaces, old.Interfaces) {
			m.state.Previous = Reading{}
		}
		if m.state.Sample.PeriodStart == p.PeriodStart {
			m.state.Sample.RX = max(m.state.Sample.RX, p.RX)
			m.state.Sample.TX = max(m.state.Sample.TX, p.TX)
			m.state.Sample.Sequence = max(m.state.Sample.Sequence, p.Sequence)
		}
		m.state.Policy = p
	}
	return m.saveLocked()
}

func boundary(month time.Time, day int) time.Time {
	last := time.Date(month.Year(), month.Month()+1, 0, 0, 0, 0, 0, month.Location()).Day()
	return time.Date(month.Year(), month.Month(), min(day, last), 0, 0, 0, 0, month.Location())
}

func Period(now time.Time, day int, zone string) (int64, int64, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil || day < 1 || day > 31 {
		return 0, 0, errors.New("invalid reset calendar")
	}
	now = now.In(loc)
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	start := boundary(month, day)
	if now.Before(start) {
		start = boundary(month.AddDate(0, -1, 0), day)
	}
	nextMonth := time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, loc)
	return start.Unix(), boundary(nextMonth, day).Unix(), nil
}

func add(a, b uint64) uint64 {
	if a >= MaxBytes || b >= MaxBytes-a {
		return MaxBytes
	}
	return a + b
}
func delta(current, previous uint64, reset bool) uint64 {
	if reset || current < previous {
		return current
	}
	return current - previous
}

func (m *Meter) Tick(now time.Time) (Sample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := &m.state.Policy
	if !m.loaded {
		return Sample{}, true
	}
	if now.Unix() >= p.PeriodEnd {
		start, end, _ := Period(now, p.ResetDay, p.Timezone)
		p.PeriodStart, p.PeriodEnd, p.Baseline = start, end, 0
		m.state.Sample = Sample{Epoch: p.Epoch, PeriodStart: start}
		if m.lost {
			m.lost = false
			m.state.Previous = Reading{}
		}
	}
	s := &m.state.Sample
	s.Sequence++
	reading, err := m.reader(p.Interfaces)
	if m.lost {
		s.Error, s.Initialized = "state_lost", false
	} else if err != nil || len(reading.Counters) == 0 || reading.Boot == "" {
		s.Error = "interfaces_unavailable"
	} else {
		s.Error = ""
		names := make([]string, 0, len(reading.Counters))
		for name, c := range reading.Counters {
			names = append(names, name)
			if m.state.Previous.Boot != "" {
				prev, ok := m.state.Previous.Counters[name]
				reset := !ok || reading.Boot != m.state.Previous.Boot || c.Identity != prev.Identity
				s.RX = add(s.RX, delta(c.RX, prev.RX, reset))
				s.TX = add(s.TX, delta(c.TX, prev.TX, reset))
			}
		}
		slices.Sort(names)
		s.Interfaces, s.Initialized = names, true
		m.state.Previous = reading
	}
	if err := m.saveLocked(); err != nil {
		s.Error = "state_write_failed"
	}
	return m.sampleLocked(), m.blockedLocked()
}

func (m *Meter) Snapshot() (Sample, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sampleLocked(), m.blockedLocked()
}
func (m *Meter) HasPolicy() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.loaded || m.lost }
func (m *Meter) sampleLocked() Sample {
	s := m.state.Sample
	s.Interfaces = append([]string{}, s.Interfaces...)
	return s
}
func (m *Meter) blockedLocked() bool {
	p, s := m.state.Policy, m.state.Sample
	if !m.loaded {
		return true
	}
	if !p.Active {
		return true
	}
	if p.Limit == 0 {
		return false
	}
	used := add(p.Baseline, s.TX)
	if p.Mode == "both" {
		used = add(used, s.RX)
	}
	return m.lost || m.writeFailed || !s.Initialized || s.Error != "" || used >= p.Limit
}

func (m *Meter) saveLocked() (err error) {
	defer func() { m.writeFailed = err != nil }()
	data, err := json.Marshal(m.state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(m.path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "machine-traffic-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), m.path); err != nil {
		return fmt.Errorf("save machine traffic: %w", err)
	}
	if d, e := os.Open(dir); e == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
