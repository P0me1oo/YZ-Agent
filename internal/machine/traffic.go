package machine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/machinetraffic"
)

func (o *Orchestrator) initTraffic(ctx context.Context, p *machinetraffic.Policy) error {
	identity := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", o.cfg.Panel.URL, o.cfg.Machine.MachineID)))
	o.trafficMeter = machinetraffic.New(filepath.Join(o.cfg.Kernel.ConfigDir, fmt.Sprintf("machine-traffic-%x.json", identity[:8])), nil)
	return o.activateTraffic(ctx, p, true)
}

func (o *Orchestrator) activateTraffic(ctx context.Context, p *machinetraffic.Policy, initial bool) error {
	if p == nil && !o.trafficMeter.HasPolicy() {
		return nil
	}
	o.trafficDecisionMu.Lock()
	defer o.trafficDecisionMu.Unlock()
	o.trafficEnabled.Store(true)
	setBlocked := o.setTrafficBlocked
	if initial {
		setBlocked = o.trafficBlocked.Store
	}
	setBlocked(true)
	if p != nil {
		if err := o.trafficMeter.Apply(*p); err != nil {
			return err
		}
	}
	sample, blocked := o.trafficMeter.Tick(time.Now())
	setBlocked(blocked)
	if next, err := o.client.ExchangeMachineTraffic(ctx, &sample); err == nil {
		if err = o.trafficMeter.Apply(*next); err != nil {
			return err
		}
		_, blocked = o.trafficMeter.Snapshot()
		setBlocked(blocked)
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
		o.trafficGeneration.Add(1)
		select {
		case o.forwardingWake <- struct{}{}:
		default:
		}
		o.logger().Info("machine traffic protection changed", "blocked", blocked)
		if !blocked && o.runCtx != nil {
			o.runJob(func() { o.rediscover(o.runCtx) })
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
	var sender sync.WaitGroup
	defer sender.Wait()
	sender.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case sample := <-reports:
				if sample.Mode == "proxy" {
					sample.ProxyWarning = o.proxyMeterWarning()
				}
				p, err := o.client.ExchangeMachineTraffic(ctx, &sample)
				o.failures.Report(o.logger(), "machine traffic exchange failed", err)
				if err != nil {
					continue
				}
				o.trafficDecisionMu.Lock()
				err = o.trafficMeter.Apply(*p)
				o.failures.Report(o.logger(), "machine traffic policy update failed", err)
				_, blocked := o.trafficMeter.Snapshot()
				o.setTrafficBlocked(blocked || (err != nil && p.Limit != 0))
				o.trafficDecisionMu.Unlock()
			}
		}
	})
	count := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !o.trafficEnabled.Load() {
				continue
			}
			o.trafficDecisionMu.Lock()
			sample, blocked := o.trafficMeter.Tick(now)
			changed := o.trafficBlocked.Load() != blocked
			o.setTrafficBlocked(blocked)
			o.trafficDecisionMu.Unlock()
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

func (o *Orchestrator) proxyMeterWarning() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, h := range o.nodes {
		if h.proxyWarning != nil {
			if warning := h.proxyWarning(); warning != "" {
				return warning
			}
		}
	}
	return ""
}
