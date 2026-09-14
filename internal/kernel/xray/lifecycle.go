package xray

import (
	"context"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	xrayCore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/inbound"
)

type retiredXray struct {
	instance   *xrayCore.Instance
	dispatcher *LimitDispatcher
	config     *model.NodeSpec
	cancel     context.CancelFunc
	stop       chan struct{}
	done       chan struct{}
}

// 调用方持有 x.mu；旧实例一直参与采样，直到连接排空并完成最终结算。
func (x *Xray) retireCurrentLocked() *retiredXray {
	if x.instance == nil {
		return nil
	}
	previous := &retiredXray{
		instance: x.instance, dispatcher: x.limitDispatcher, config: x.nodeConfig, cancel: x.cancel,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	x.retired = append(x.retired, previous)
	return previous
}

// 先同步关闭所有旧监听，再在后台等待已有连接，保证 Reload 返回后旧端口不接收新连接。
func (x *Xray) closeOld(previous *retiredXray) {
	if previous == nil {
		return
	}
	closeXrayListeners(previous.instance)
	go x.recycleOld(previous)
}

func closeXrayListeners(instance *xrayCore.Instance) {
	if manager, ok := instance.GetFeature(inbound.ManagerType()).(inbound.Manager); ok {
		ctx := context.Background()
		for _, handler := range manager.ListHandlers(ctx) {
			if handler.Tag() == "" {
				_ = handler.Close()
			} else if err := manager.RemoveHandler(ctx, handler.Tag()); err != nil {
				nlog.Core().Warn("close old xray inbound failed", "error", err)
			}
		}
	}
}

func (x *Xray) recycleOld(previous *retiredXray) {
	defer close(previous.done)
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	stop := previous.stop
drain:
	for previous.dispatcher != nil && previous.dispatcher.connCount.Load() > 0 {
		select {
		case <-ticker.C:
		case <-stop:
			// 退出时所有旧实例共同使用短排空窗口，避免等待每次重载的五分钟期限。
			timer.Reset(drainTimeout)
			stop = nil
		case <-timer.C:
			break drain
		}
	}
	// 在关闭实例前读取最后一段统计；Close 可能同时释放 stats manager。
	x.mu.Lock()
	x.collectInstanceStatsLocked(previous.instance, previous.config)
	x.mu.Unlock()
	if previous.cancel != nil {
		previous.cancel()
	}
	_ = previous.instance.Close()
	if previous.dispatcher != nil {
		drainConns(previous.dispatcher, time.Second)
	}

	x.mu.Lock()
	x.collectInstanceStatsLocked(previous.instance, previous.config)
	for i, candidate := range x.retired {
		if candidate == previous {
			x.retired = append(x.retired[:i], x.retired[i+1:]...)
			break
		}
	}
	x.mu.Unlock()
	nlog.Core().Debug("xray: old instance recycled")
}
