package service

import (
	"context"
	"errors"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"testing"
)

type accessTestSink struct {
	enabled bool
	fail    bool
	batches []string
}

func (s *accessTestSink) AccessLogEnabled() bool { return s.enabled }
func (s *accessTestSink) ReportAccessLog(_ context.Context, batch *accesslog.Batch) error {
	s.batches = append(s.batches, batch.ID)
	if s.fail {
		return errors.New("测试中断")
	}
	return nil
}

func TestAccessLogFlushRetryRestoresExactBatch(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{}}
	s.accessLog.Enable(true)
	s.accessLog.Add(9, "example.test", "", 443)
	sink := &accessTestSink{enabled: true, fail: true}
	s.flushAccessLog(t.Context(), sink)
	if len(sink.batches) != 1 {
		t.Fatal("没有尝试上报")
	}
	restored := &Service{cfg: cfg, accessLog: &accesslog.Recorder{}}
	if err := restored.accessLog.Restore(restored.accessLogPath()); err != nil {
		t.Fatal(err)
	}
	sink.fail = false
	restored.flushAccessLog(t.Context(), sink)
	if len(sink.batches) != 2 || sink.batches[0] != sink.batches[1] {
		t.Fatal("重启重试改变了批次")
	}
	if batch, _ := restored.accessLog.Next(); batch != nil {
		t.Fatal("确认后记录仍未清理")
	}
}
