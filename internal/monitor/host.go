package monitor

import (
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
)

const (
	hostInterval = 10 * time.Second
	hostMaxGap   = 3 * hostInterval
)

// HostSample describes the host visible to the agent. A nil measurement is
// unavailable. Network rates are bytes per second across the selected interfaces.
// Snapshots, including their optional values, are read-only after publication.
type HostSample struct {
	SampledAt              int64    `json:"sampled_at"`
	CPUPercent             *float64 `json:"cpu_percent"`
	MemoryUsedBytes        *uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes       *uint64  `json:"memory_total_bytes"`
	DiskUsedBytes          *uint64  `json:"disk_used_bytes"`
	DiskTotalBytes         *uint64  `json:"disk_total_bytes"`
	UploadBytesPerSecond   *float64 `json:"upload_bytes_per_second"`
	DownloadBytesPerSecond *float64 `json:"download_bytes_per_second"`
}

type cpuCounters struct {
	busy, idle float64
}

type linkCounter struct {
	index  int
	rx, tx uint64
}

type hostCounters struct {
	cpu   *cpuCounters
	links map[string]linkCounter
}

type hostSampler struct {
	mu       sync.Mutex
	at       time.Time
	previous hostCounters
	latest   HostSample
}

var sharedHost hostSampler

// CollectHostSample shares one measurement between the machine and its nodes.
func CollectHostSample() HostSample {
	sharedHost.mu.Lock()
	defer sharedHost.mu.Unlock()
	now := time.Now()
	// A few milliseconds of jitter on the 5s control ticker must not delay a
	// due sample until the following heartbeat. Rates always use actual elapsed time.
	if !sharedHost.at.IsZero() && now.Sub(sharedHost.at) < hostInterval-100*time.Millisecond {
		return sharedHost.latest
	}
	return sharedHost.collect(now)
}

func (s *hostSampler) collect(now time.Time) HostSample {
	result := HostSample{SampledAt: now.Unix()}
	if total, used, ok := readMemory(); ok {
		result.MemoryTotalBytes, result.MemoryUsedBytes = &total, &used
	}
	if usage, err := disk.Usage("/"); err == nil && usage.Total > 0 && usage.Used <= usage.Total {
		result.DiskTotalBytes, result.DiskUsedBytes = &usage.Total, &usage.Used
	}
	next := hostCounters{cpu: readCPU(), links: readLinks()}
	s.applyDeltas(now, next, &result)
	s.latest = result
	return result
}

func readCPU() *cpuCounters {
	values, err := cpu.Times(false)
	if err != nil || len(values) != 1 {
		return nil
	}
	v := values[0]
	// Linux already includes guest ticks in user/nice. Exclude idle and I/O wait.
	return &cpuCounters{
		busy: v.User + v.Nice + v.System + v.Irq + v.Softirq + v.Steal,
		idle: v.Idle + v.Iowait,
	}
}

func (s *hostSampler) applyDeltas(now time.Time, next hostCounters, result *HostSample) {
	previous := s.previous
	elapsed := now.Sub(s.at)
	validInterval := !s.at.IsZero() && elapsed > 0 && elapsed <= hostMaxGap
	s.at, s.previous = now, next
	if !validInterval {
		return
	}
	if a, b := previous.cpu, next.cpu; a != nil && b != nil {
		busy, idle := b.busy-a.busy, b.idle-a.idle
		if busy >= 0 && idle >= 0 && busy+idle > 0 {
			percent := 100 * busy / (busy + idle)
			result.CPUPercent = &percent
		}
	}
	if len(next.links) == 0 || len(previous.links) != len(next.links) {
		return
	}
	var rx, tx float64
	for name, link := range next.links {
		old, ok := previous.links[name]
		if !ok || old.index != link.index || old.rx > link.rx || old.tx > link.tx {
			return
		}
		rx += float64(link.rx - old.rx)
		tx += float64(link.tx - old.tx)
	}
	down, up := rx/elapsed.Seconds(), tx/elapsed.Seconds()
	result.UploadBytesPerSecond, result.DownloadBytesPerSecond = &up, &down
}
