package monitor

import (
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

var startTime = time.Now()

// Status holds system resource metrics
type Status struct {
	HostSkipped bool
	Uptime      uint64
	CPU         float64
	CPUPerCore  []float64
	Load1       float64
	Load5       float64
	Load15      float64
	MemTotal    uint64
	MemUsed     uint64
	SwapTotal   uint64
	SwapUsed    uint64
	DiskTotal   uint64
	DiskUsed    uint64
	Goroutines  int

	// Network speed (bytes/sec), -1 means unavailable (first sample).
	NetInSpeed  float64
	NetOutSpeed float64

	// GC metrics (process-wide)
	NumGC       uint32
	LastPauseMS float64
}

// Collect 复用主机快照；进程和逐核诊断按秒刷新。
func Collect() Status {
	return systemSampler.sample()
}

// CollectWithHost 关闭主机监控时只读取 Agent 自身的运行时信息。
func CollectWithHost(enabled bool) Status {
	if enabled {
		return Collect()
	}
	s := processSampler.sample()
	s.HostSkipped = true
	return s
}

func collect() Status {
	s := processSampler.sample()

	host := CollectHostSample()
	if host.CPUPercent != nil {
		s.CPU = *host.CPUPercent
	}
	if host.MemoryTotalBytes != nil {
		s.MemTotal, s.MemUsed = *host.MemoryTotalBytes, *host.MemoryUsedBytes
	}
	if host.DiskTotalBytes != nil {
		s.DiskTotal, s.DiskUsed = *host.DiskTotalBytes, *host.DiskUsedBytes
	}
	s.NetInSpeed, s.NetOutSpeed = -1, -1
	if host.DownloadBytesPerSecond != nil {
		s.NetInSpeed, s.NetOutSpeed = *host.DownloadBytesPerSecond, *host.UploadBytesPerSecond
	}

	// Per-core CPU usage (best-effort; safe if it fails).
	if perCore, err := cpu.Percent(0, true); err == nil && len(perCore) > 0 {
		s.CPUPerCore = perCore
	}

	if loadAvg, err := load.Avg(); err == nil {
		s.Load1 = loadAvg.Load1
		s.Load5 = loadAvg.Load5
		s.Load15 = loadAvg.Load15
	}

	if swapStat, err := mem.SwapMemory(); err == nil {
		s.SwapTotal = swapStat.Total
		s.SwapUsed = swapStat.Used
	}

	return s
}

func processStatus() Status {
	s := Status{Uptime: uint64(time.Since(startTime).Seconds())}
	// GC metrics
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	s.Goroutines = runtime.NumGoroutine()
	s.NumGC = ms.NumGC
	if ms.NumGC > 0 {
		// PauseNs is a ring buffer of the most recent GC pause times.
		idx := (ms.NumGC - 1) % uint32(len(ms.PauseNs))
		s.LastPauseMS = float64(ms.PauseNs[idx]) / 1e6
	}

	return s
}
