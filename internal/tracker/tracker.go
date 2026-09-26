package tracker

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

// snapshot is an immutable point-in-time view of tracker state.
// It is swapped atomically so readers never block writers.
type snapshot struct {
	traffic   map[int][2]int64        // userID → [upload, download] delta
	aliveIPs  map[int]map[string]bool // userID → set of source IPs（含不计入设备数的来源）
	online    map[int]int             // userID → distinct IP count（含不计入设备数的来源）
	connCount int
	inSpeed   int64 // 字节每秒
	outSpeed  int64 // 字节每秒
}

// Tracker computes per-user traffic deltas from cumulative counters
// provided by the kernel, and accumulates totals for panel reporting.
//
// Architecture: the kernel maintains per-user atomic counters and IP sets.
// Each tick, the service calls Process() with cumulative per-user traffic.
// Tracker computes deltas against the previous cycle's values — O(users),
// not O(connections).
//
// Thread safety: Process() acquires mu to update internal state, then
// atomically publishes a new snapshot. All read methods (Flush*,
// CurrentOnline, LogStats, *Speed) read the snapshot lock-free.
// This eliminates contention between the 10s Process tick and the 60s
// flush/push tick.
type Tracker struct {
	mu sync.Mutex

	// lastSeen stores the cumulative traffic from the previous Process() call.
	// Protected by mu — only written by Process.
	lastSeen map[int][2]int64 // userID → [upload, download] cumulative

	// pending accumulates deltas between flushes.
	// Protected by mu — written by Process, drained by FlushTraffic.
	pendingTraffic map[int][2]int64

	// Relay bookkeeping mirrors the user maps but is keyed by logical node ID
	// and reported on a separate channel, so landing-line traffic never mixes
	// into user quota accounting. Protected by mu.
	lastSeenRelay map[int][2]int64
	pendingRelay  map[int][2]int64

	// Per-user relay bookkeeping is keyed by user ID and logical node ID.
	// It remains separate from aggregate relay traffic for independent retries.
	lastSeenRelayUser map[int]map[int][2]int64
	pendingRelayUser  map[int]map[int][2]int64

	// live holds the current snapshot, swapped atomically.
	// Readers load this pointer without any lock.
	live atomic.Pointer[snapshot]

	// lastProcess 是上次采样的时间，用实际间隔换算速度，与采样周期配置无关。
	lastProcess time.Time
}

func New() *Tracker {
	t := &Tracker{
		lastSeen:          make(map[int][2]int64),
		pendingTraffic:    make(map[int][2]int64),
		lastSeenRelay:     make(map[int][2]int64),
		pendingRelay:      make(map[int][2]int64),
		lastSeenRelayUser: make(map[int]map[int][2]int64),
		pendingRelayUser:  make(map[int]map[int][2]int64),
		lastProcess:       time.Now(),
	}
	// Publish initial empty snapshot.
	t.live.Store(&snapshot{
		traffic:  make(map[int][2]int64),
		aliveIPs: make(map[int]map[string]bool),
		online:   make(map[int]int),
	})
	return t
}

// Process computes per-user traffic deltas from cumulative kernel counters.
// Also stores alive IPs and connection count. O(users).
//
// This is the only writer to lastSeen and pendingTraffic.
// After computing deltas, it publishes a new snapshot for lock-free reads.
func (t *Tracker) Process(
	cumTraffic map[int][2]int64,
	kernelAliveIPs map[int]map[string]bool,
	connCount int,
) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var cycleIn, cycleOut int64

	for uid, cum := range cumTraffic {
		prev := t.lastSeen[uid]
		deltaUp := cum[0] - prev[0]
		deltaDown := cum[1] - prev[1]

		// Guard against counter reset (kernel restart).
		if deltaUp < 0 {
			deltaUp = cum[0]
		}
		if deltaDown < 0 {
			deltaDown = cum[1]
		}

		t.lastSeen[uid] = cum

		if deltaUp > 0 || deltaDown > 0 {
			cur := t.pendingTraffic[uid]
			cur[0] += deltaUp
			cur[1] += deltaDown
			t.pendingTraffic[uid] = cur

			cycleOut += deltaUp
			cycleIn += deltaDown
		}
	}

	now := time.Now()
	elapsed := now.Sub(t.lastProcess).Seconds()
	t.lastProcess = now
	inSpeed, outSpeed := perSecond(cycleIn, elapsed), perSecond(cycleOut, elapsed)

	// Compute online from alive IPs.
	online := make(map[int]int, len(kernelAliveIPs))
	for uid, ips := range kernelAliveIPs {
		online[uid] = len(ips)
	}

	// Publish new snapshot (readers will see this atomically).
	t.live.Store(&snapshot{
		traffic:   copyTrafficMap(t.pendingTraffic),
		aliveIPs:  kernelAliveIPs, // kernel provides fresh copy each tick
		online:    online,
		connCount: connCount,
		inSpeed:   inSpeed,
		outSpeed:  outSpeed,
	})
}

// FlushTraffic returns accumulated per-user traffic and resets the pending buffer.
// Lock-free: reads from live snapshot, then acquires mu only to drain.
func (t *Tracker) FlushTraffic() map[int][2]int64 {
	t.mu.Lock()
	data := t.pendingTraffic
	t.pendingTraffic = make(map[int][2]int64, len(data))
	t.mu.Unlock()
	return data
}

// RestoreTraffic adds traffic back (used when push to panel fails).
func (t *Tracker) RestoreTraffic(data map[int][2]int64) {
	t.mu.Lock()
	for uid, d := range data {
		cur := t.pendingTraffic[uid]
		cur[0] += d[0]
		cur[1] += d[1]
		t.pendingTraffic[uid] = cur
	}
	t.mu.Unlock()
}

// HasTraffic returns true if there is accumulated traffic to report.
func (t *Tracker) HasTraffic() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pendingTraffic) > 0
}

// PendingSnapshot 复制尚未刷出的累计流量，不清空缓冲，用于落盘保存。
func (t *Tracker) PendingSnapshot() (traffic, relay map[int][2]int64, relayUser map[int]map[int][2]int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.pendingTraffic) > 0 {
		traffic = copyTrafficMap(t.pendingTraffic)
	}
	if len(t.pendingRelay) > 0 {
		relay = copyTrafficMap(t.pendingRelay)
	}
	if len(t.pendingRelayUser) > 0 {
		relayUser = make(map[int]map[int][2]int64, len(t.pendingRelayUser))
		for uid, nodes := range t.pendingRelayUser {
			if len(nodes) > 0 {
				relayUser[uid] = copyTrafficMap(nodes)
			}
		}
		if len(relayUser) == 0 {
			relayUser = nil
		}
	}
	return traffic, relay, relayUser
}

// FlushAliveIPs 返回当前权威设备快照。每次都返回完整副本，包括空快照，
// 让面板能够续期稳定在线设备并清理已经离线的用户。
// 保留完整公网来源，面板在原始地址上过滤名单后再合并 IPv6 网段。
func (t *Tracker) FlushAliveIPs() map[int][]string {
	s := t.live.Load()
	devices := make(map[int][]string, len(s.aliveIPs))
	for uid, ips := range s.aliveIPs {
		buf := make([]string, 0, len(ips))
		seen := make(map[string]bool, len(ips))
		for ip := range ips {
			if key := deviceip.PublicAddress(ip); key != "" && !seen[key] {
				seen[key] = true
				buf = append(buf, key)
			}
		}
		if len(buf) > 0 {
			devices[uid] = buf
		}
	}
	return devices
}

// CurrentOnline returns a snapshot copy of user_id → device count (distinct IPs).
// Lock-free: reads from live snapshot.
func (t *Tracker) CurrentOnline() map[int]int {
	s := t.live.Load()
	cp := make(map[int]int, len(s.online))
	for k, v := range s.online {
		cp[k] = v
	}
	return cp
}

// LogStats logs current tracking statistics.
// Lock-free: reads from live snapshot.
func (t *Tracker) LogStats() {
	s := t.live.Load()
	nlog.TrackerStats(s.connCount, len(s.online))
}

// ActiveConnections returns the last observed active connection count.
// Lock-free: reads from live snapshot.
func (t *Tracker) ActiveConnections() int {
	return t.live.Load().connCount
}

// TotalConnections is deprecated — no longer tracked per-connection.
// Returns 0 for backward compatibility.
func (t *Tracker) TotalConnections() int64 {
	return 0
}

// InboundSpeed returns the last observed inbound (download) speed in bytes/second.
// Lock-free: reads from live snapshot.
func (t *Tracker) InboundSpeed() int64 {
	return t.live.Load().inSpeed
}

// OutboundSpeed returns the last observed outbound (upload) speed in bytes/second.
// Lock-free: reads from live snapshot.
func (t *Tracker) OutboundSpeed() int64 {
	return t.live.Load().outSpeed
}

// perSecond 按两次采样的实际间隔把字节数换算为每秒速度。
func perSecond(bytes int64, seconds float64) int64 {
	if bytes <= 0 || seconds <= 0 {
		return 0
	}
	return int64(float64(bytes) / seconds)
}

// copyTrafficMap creates a shallow copy of the traffic map.
func copyTrafficMap(src map[int][2]int64) map[int][2]int64 {
	dst := make(map[int][2]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
