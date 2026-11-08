package xray

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
)

func (d *LimitDispatcher) IPConnectionSnapshot() map[int]map[string]int {
	counts := make(map[int]map[string]int)
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.userIPs.Range(func(key, value any) bool {
		uid := d.emailToUID[key.(string)]
		if uid <= 0 {
			return true
		}
		counter := value.(*ipCounter)
		counter.mu.Lock()
		defer counter.mu.Unlock()
		for source, n := range counter.ips {
			if ip := deviceip.PublicAddress(source); ip != "" && n > 0 {
				if counts[uid] == nil {
					counts[uid] = make(map[string]int)
				}
				counts[uid][ip] += n
			}
		}
		return true
	})
	return counts
}

// 热重载后仍计入排空中的旧连接，按真实入口统计一次。
func (x *Xray) GetIPConnectionSnapshot(context.Context) (map[int]map[string]int, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	counts := make(map[int]map[string]int)
	merge := func(d *LimitDispatcher) {
		if d == nil {
			return
		}
		for uid, ips := range d.IPConnectionSnapshot() {
			if counts[uid] == nil {
				counts[uid] = make(map[string]int)
			}
			for ip, n := range ips {
				counts[uid][ip] += n
			}
		}
	}
	merge(x.limitDispatcher)
	for _, previous := range x.retired {
		merge(previous.dispatcher)
	}
	return counts, nil
}

var _ kernel.IPConnectionSnapshotReader = (*Xray)(nil)
