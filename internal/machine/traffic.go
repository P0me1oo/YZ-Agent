package machine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/machinetraffic"
)

func (o *Orchestrator) initTraffic(ctx context.Context, p *machinetraffic.Policy) error {
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", o.cfg.Panel.URL, o.cfg.Machine.MachineID)))
	o.trafficMeter = machinetraffic.New(filepath.Join(o.cfg.Kernel.ConfigDir, fmt.Sprintf("machine-traffic-%x.json", identity[:8])), nil)
	if p == nil && !o.trafficMeter.HasPolicy() {
		o.trafficMeter = nil
		return nil
	}
	o.trafficBlocked.Store(true)
	if p != nil {
		if err := o.trafficMeter.Apply(*p); err != nil {
			return err
		}
	}
	sample, blocked := o.trafficMeter.Tick(time.Now())
	o.trafficBlocked.Store(blocked)
	if next, err := o.client.ExchangeMachineTraffic(ctx, &sample); err == nil {
		if err = o.trafficMeter.Apply(*next); err != nil {
			return err
		}
		_, blocked = o.trafficMeter.Snapshot()
		o.trafficBlocked.Store(blocked)
	}
	return nil
}

// 与节点发现共用启动门禁，只取消代理服务的上下文，管理长连接持续工作。
func (o *Orchestrator) setTrafficBlocked(blocked bool) {
	o.mu.Lock()
	changed := o.trafficBlocked.Swap(blocked) != blocked
	if blocked {
		for _, h := range o.nodes {
			if h.protect != nil {
				h.protect()
			} else {
				h.cancel()
			}
		}
	}
	o.mu.Unlock()
	if changed {
		o.logger().Info("machine traffic protection changed", "blocked", blocked)
		if !blocked && o.runCtx != nil {
			go o.rediscover(o.runCtx)
		}
	}
}

func (o *Orchestrator) trafficLoop(ctx context.Context) {
	if o.trafficMeter == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// 请求在独立协程发送，网络超时不能阻塞本机额度检查。
	reports := make(chan machinetraffic.Sample, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sample := <-reports:
				p, err := o.client.ExchangeMachineTraffic(ctx, &sample)
				if err != nil {
					continue
				}
				if err = o.trafficMeter.Apply(*p); err != nil {
					o.setTrafficBlocked(true)
					continue
				}
				_, blocked := o.trafficMeter.Snapshot()
				o.setTrafficBlocked(blocked)
			}
		}
	}()
	count := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			sample, blocked := o.trafficMeter.Tick(now)
			changed := o.trafficBlocked.Load() != blocked
			o.setTrafficBlocked(blocked)
			count++
			if changed || count%5 == 0 {
				select {
				case <-reports:
				default:
				}
				select {
				case reports <- sample:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
