package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func (s *Service) blockLogPath() string {
	if s.cfg.IsStandalone() || s.cfg.Kernel.ConfigDir == "" {
		return ""
	}
	identity := fmt.Sprintf("%s|machine=%d|node=%d", strings.TrimRight(strings.TrimSpace(s.cfg.Panel.URL), "/"), s.cfg.Panel.MachineID, s.cfg.Panel.NodeID)
	return filepath.Join(s.cfg.Kernel.ConfigDir, fmt.Sprintf("pending-block-%x.json", sha256.Sum256([]byte(identity))))
}

func (s *Service) initializeBlockLog() {
	sink, ok := s.sink.(controlplane.BlockLogSink)
	if !ok || s.blockLog == nil {
		return
	}
	if err := s.blockLog.Restore(s.blockLogPath()); err != nil {
		nlog.Core().Warn("路由拦截记录缓存恢复失败，保留原文件", "error", err)
		// 不覆盖用途不明或损坏的缓存，代理服务仍可继续运行。
		s.blockLogFailed = true
		return
	}
	s.blockLog.Enable(sink.BlockLogEnabled())
}

// 上报有独立的协程和超时，不与流量结算或连接回调相互等待。
func (s *Service) startBlockLog(ctx context.Context) func() {
	sink, ok := s.sink.(controlplane.BlockLogSink)
	if !ok || s.blockLog == nil || s.blockLogFailed {
		return func() {}
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-run.Done():
				return
			case <-ticker.C:
				s.blockLog.Enable(sink.BlockLogEnabled())
				flush, stop := context.WithTimeout(run, 10*time.Second)
				s.flushBlockLog(flush, sink)
				stop()
			}
		}
	}()
	return func() {
		cancel()
		<-done
		// Run 的退出分支已停止核心；这里只停止采集并保存最后的队列。
		s.blockLog.Enable(false)
		flush, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		s.flushBlockLog(flush, sink)
	}
}

func (s *Service) flushBlockLog(ctx context.Context, sink controlplane.BlockLogSink) {
	path := s.blockLogPath()
	defer func() {
		if err := s.blockLog.Save(path); err != nil {
			nlog.Core().Warn("路由拦截记录缓存保存失败", "error", err)
		}
		if count := s.blockLog.Dropped(); count > 0 {
			nlog.Core().Warn("路由拦截记录队列已满，部分记录未保存", "count", count)
		}
	}()
	if _, err := s.blockLog.Next(); err != nil {
		return
	}
	count, err := s.blockLog.Checkpoint(path)
	if err != nil {
		nlog.Core().Warn("路由拦截记录缓存保存失败", "error", err)
		return
	}
	for i := 0; i < count && ctx.Err() == nil; i++ {
		batch, err := s.blockLog.Next()
		if err != nil {
			nlog.Core().Warn("路由拦截记录批次创建失败")
			return
		}
		if batch == nil || !sink.BlockLogEnabled() {
			return
		}
		if err = sink.ReportBlockLog(ctx, batch); err != nil {
			nlog.Core().Warn("路由拦截记录上报未确认", "error", err)
			return
		}
		s.blockLog.Ack(batch.ID)
	}
}
