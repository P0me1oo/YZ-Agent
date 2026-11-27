package xray

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
)

func (x *Xray) ReadUserUsage(_ context.Context, ids []int) kernel.UsageSample {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.collectStatsLocked()
	sample := kernel.NewUsageSample(ids)
	sample.Traffic.Users = copyTraffic(x.cumTraffic)
	sample.Traffic.Relay = copyTraffic(x.cumRelayTraffic)
	sample.Traffic.RelayUsers = x.copyRelayUserTrafficLocked()
	online := make(map[int]struct{})
	add := func(d *LimitDispatcher) {
		if d == nil {
			return
		}
		d.mu.RLock()
		defer d.mu.RUnlock()
		sample.Connections += int(d.connCount.Load())
		d.userIPs.Range(func(key, value any) bool {
			id := d.emailToUID[key.(string)]
			if id <= 0 {
				return true
			}
			counter := value.(*ipCounter)
			counter.mu.Lock()
			defer counter.mu.Unlock()
			if len(counter.ips) == 0 {
				return true
			}
			online[id] = struct{}{}
			if row := sample.Users[id]; row != nil {
				for ip, n := range counter.ips {
					if n > 0 {
						row.Count += n
						row.AddAddress(ip)
					}
				}
			}
			return true
		})
	}
	add(x.limitDispatcher)
	for _, previous := range x.retired {
		add(previous.dispatcher)
	}
	sample.OnlineUsers = len(online)
	return sample
}
