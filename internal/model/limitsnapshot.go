package model

import (
	"net/netip"
	"slices"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/limitreport"
)

// LimitSnapshot 保存一次拒绝对应的来源和名额快照，不能用发送时的在线状态代替。
type LimitSnapshot = limitreport.Snapshot

// LimitSnapshotReporter 是可选扩展；原有准入实现无需增加方法。
// build 在统计锁之外同步调用，调用者负责保护来源列表。
type LimitSnapshotReporter interface {
	ReportLimitedSnapshot(userID int, kind string, limit, observed int, sourceIP string, build func() *LimitSnapshot)
}

func ReportLimit(reporter ConnLimiter, userID int, kind string, limit, observed int, sourceIP string, build func() *LimitSnapshot) {
	if extended, ok := reporter.(LimitSnapshotReporter); ok {
		extended.ReportLimitedSnapshot(userID, kind, limit, observed, sourceIP, build)
	} else if kind == ConnLimitKindDevice {
		if device, ok := reporter.(DeviceLimitReporter); ok {
			device.ReportDeviceLimited(userID, limit, observed, sourceIP)
		}
	} else if reporter != nil {
		reporter.ReportLimited(userID, kind, limit, observed)
	}
}

func NewLimitSnapshot(source string, active []string, known bool) *LimitSnapshot {
	set := make(map[string]bool)
	for _, raw := range active {
		if ip, err := netip.ParseAddr(raw); err == nil {
			set[ip.Unmap().String()] = true
		} else if prefix, err := netip.ParsePrefix(raw); err == nil && prefix.Addr().Is6() && prefix.Bits() == 64 {
			set[prefix.Masked().String()] = true
		}
	}
	ips := make([]string, 0, len(set))
	for ip := range set {
		ips = append(ips, ip)
	}
	slices.Sort(ips)
	total := len(ips)
	if len(ips) > 5 {
		ips = ips[:5]
	}
	return &LimitSnapshot{OccurredAt: time.Now().Unix(), SourceIP: source, ActiveIPs: ips, ActiveTotal: total, ActiveKnown: known}
}

// 旧设备快照中的 IPv6 已按网段归并，明确保留 /64，避免冒充完整来源地址。
func DeviceLimitSources(keys map[string]bool) []string {
	ips := make([]string, 0, len(keys))
	for key := range keys {
		if ip, err := netip.ParseAddr(key); err == nil && ip.Is6() {
			key = netip.PrefixFrom(ip, 64).Masked().String()
		}
		ips = append(ips, key)
	}
	return ips
}
