package singbox

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
)

// 使用连接引用数，保留完整 IPv6 地址；设备数的 /64 合并不参与连接统计。
func (s *SingBox) GetIPConnectionSnapshot(context.Context) (map[int]map[string]int, error) {
	ct := s.connTrackerSafe()
	if ct == nil {
		return nil, nil
	}
	counts := make(map[int]map[string]int)
	ct.usersMu.RLock()
	defer ct.usersMu.RUnlock()
	for uid, stats := range ct.users {
		stats.mu.RLock()
		for source, n := range stats.ips {
			if ip := deviceip.PublicAddress(source); ip != "" && n > 0 {
				if counts[uid] == nil {
					counts[uid] = make(map[string]int)
				}
				counts[uid][ip] += n
			}
		}
		stats.mu.RUnlock()
	}
	return counts, nil
}

var _ kernel.IPConnectionSnapshotReader = (*SingBox)(nil)
