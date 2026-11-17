package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type accessTestSink struct {
	controlplane.Sink
	enabled  bool
	revision uint64
	fail     bool
	onReport func()
	batches  []string
	records  []accesslog.Record
}

func TestAccessLogDisabledDiscardsMemoryAndDiskBeforeReenable(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}}
	s.accessLog.Enable(true)
	s.accessLog.Add(9, "before-disable.example.test", "", 443)
	if err := s.accessLog.Save(s.accessLogPath()); err != nil {
		t.Fatal(err)
	}
	sink := &accessTestSink{}
	s.flushAccessLog(t.Context(), sink)
	s.accessLog.Add(9, "while-disabled.example.test", "", 443)
	if batch, _ := s.accessLog.Next(); batch != nil {
		t.Fatal("关闭后仍保留或新增访问记录缓存")
	}
	if _, err := os.Stat(s.accessLogPath()); !os.IsNotExist(err) {
		t.Fatalf("关闭后访问记录磁盘缓存未清除：%v", err)
	}
	if len(sink.batches) != 0 {
		t.Fatal("关闭后仍尝试上报")
	}
	sink.enabled = true
	s.accessLog.Enable(true)
	s.flushAccessLog(t.Context(), sink)
	s.accessLog.Add(9, "after-enable.example.test", "", 443)
	s.flushAccessLog(t.Context(), sink)
	if len(sink.records) != 1 || sink.records[0].Destination != "after-enable.example.test" {
		t.Fatal("重新开启没有恢复采集，或补报了关闭前的缓存")
	}
}

func (s *accessTestSink) AccessLogEnabled() bool         { return s.enabled }
func (s *accessTestSink) AccessLogState() (bool, uint64) { return s.enabled, s.revision }
func (s *accessTestSink) ReportAccessLog(_ context.Context, batch *accesslog.Batch) error {
	s.batches = append(s.batches, batch.ID)
	s.records = append(s.records, batch.Records...)
	if s.onReport != nil {
		s.onReport()
	}
	if s.fail {
		return errors.New("测试中断")
	}
	return nil
}

func TestAccessLogDisabledStartupClearsCacheWithoutDecrypting(t *testing.T) {
	for _, content := range []string{"invalid-json", "YZENC1\ninvalid-encrypted-cache"} {
		t.Run(content, func(t *testing.T) {
			cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
			sink := &accessTestSink{}
			s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
			if err := os.WriteFile(s.accessLogPath(), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			s.initializeAccessLog()
			if _, err := os.Stat(s.accessLogPath()); !os.IsNotExist(err) || s.accessLogFailed {
				t.Fatalf("停用启动未清除缓存，或被缓存解密失败阻塞：%v", err)
			}
			sink.enabled = true
			s.accessLog.Enable(true)
			s.accessLog.Add(9, "new.example.test", "", 443)
			s.flushAccessLog(t.Context(), sink)
			if len(sink.records) != 1 {
				t.Fatal("停用启动后无法重新采集")
			}
		})
	}
}

func TestAccessLogPolicyStopsCallbacksAndClearsFailedInflightBatch(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	sink := &accessTestSink{enabled: true, fail: true}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	s.initializeAccessLog()
	s.accessLog.Add(9, "old.example.test", "", 443)
	sink.onReport = func() { sink.enabled = false; sink.revision++ }
	s.flushAccessLog(t.Context(), sink)
	if _, err := os.Stat(s.accessLogPath()); !os.IsNotExist(err) {
		t.Fatalf("上报期间停用后保留了待确认缓存：%v", err)
	}
	s.accessLog.Add(9, "disabled.example.test", "", 443)
	if batch, _ := s.accessLog.Next(); batch != nil {
		t.Fatal("运行期间停用未阻止采集")
	}
	sink.enabled, sink.fail, sink.onReport = true, false, nil
	s.accessLog.Enable(true)
	s.accessLog.Add(9, "new.example.test", "", 443)
	s.flushAccessLog(t.Context(), sink)
	if len(sink.records) != 2 || sink.records[1].Destination != "new.example.test" {
		t.Fatal("重新开启补报了旧记录")
	}
}

func TestAccessLogCorruptCachePreservedUntilExplicitDisable(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	sink := &accessTestSink{enabled: true}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	if err := os.WriteFile(s.accessLogPath(), []byte("corrupt-cache"), 0600); err != nil {
		t.Fatal(err)
	}
	s.initializeAccessLog()
	s.flushAccessLog(t.Context(), sink)
	data, err := os.ReadFile(s.accessLogPath())
	if err != nil || string(data) != "corrupt-cache" || !s.accessLogFailed {
		t.Fatal("开启期间覆盖了损坏缓存")
	}
	sink.enabled = false
	s.flushAccessLog(t.Context(), sink)
	if _, err := os.Stat(s.accessLogPath()); !os.IsNotExist(err) || s.accessLogFailed {
		t.Fatal("明确停用后仍保留损坏缓存或无法恢复")
	}
}

func TestAccessLogFinalFlushDoesNotRestartCollection(t *testing.T) {
	sink := &accessTestSink{enabled: true}
	s := &Service{cfg: &config.Config{}, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	s.accessLog.Enable(true)
	s.accessLog.Add(9, "last.example.test", "", 443)
	s.accessLog.Enable(false)
	s.flushAccessLog(t.Context(), sink)
	s.accessLog.Add(9, "after-stop.example.test", "", 443)
	if s.accessLog.Enabled() || len(sink.records) != 1 {
		t.Fatal("退出保存重新开启了采集")
	}
	if batch, _ := s.accessLog.Next(); batch != nil {
		t.Fatal("退出后仍采集访问记录")
	}
}

func TestAccessLogCorruptCacheClearedAfterRapidDisableReenable(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	sink := &accessTestSink{enabled: true}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	if err := os.WriteFile(s.accessLogPath(), []byte("corrupt-cache"), 0600); err != nil {
		t.Fatal(err)
	}
	s.initializeAccessLog()
	sink.enabled = false
	sink.revision++
	sink.enabled = true
	s.flushAccessLog(t.Context(), sink)
	if _, err := os.Stat(s.accessLogPath()); !os.IsNotExist(err) || s.accessLogFailed {
		t.Fatal("快速重新开启仍保留已停用的损坏缓存")
	}
}

func TestAccessLogRebuildKeepsValidQueueAfterReenable(t *testing.T) {
	cfg := &config.Config{
		Panel:  config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7},
		Kernel: config.KernelConfig{ConfigDir: t.TempDir()},
	}
	sink := &accessTestSink{enabled: true, revision: 1}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	s.initializeAccessLog()
	s.accessLog.Add(9, "after-enable.example.test", "", 443)
	first, _ := s.accessLog.Next()
	if err := s.accessLog.Save(s.accessLogPath()); err != nil {
		t.Fatal(err)
	}
	rebuilt := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
	rebuilt.initializeAccessLog()
	rebuilt.flushAccessLog(t.Context(), sink)
	if len(sink.batches) != 1 || sink.batches[0] != first.ID {
		t.Fatal("节点重建清除了重新开启后的有效待报批次")
	}
}

func TestAccessLogGenerationSurvivesCrashAndMachineNodeRebuild(t *testing.T) {
	for _, name := range []string{"crash before cleanup", "disabled while offline", "machine node rebuild", "new process keeps valid queue"} {
		t.Run(name, func(t *testing.T) {
			var phase atomic.Int32
			var reports atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v2/server/handshake" {
					step := phase.Load()
					generation := ""
					if step > 0 {
						generation = strings.Repeat("a", 32)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"access_log": map[string]any{"version": 2, "enabled": step != 1, "generation": generation},
					})
					return
				}
				var batch panel.AgentLogBatch
				if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
					t.Error(err)
				}
				reports.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"batch_id": batch.ID}})
			}))
			defer server.Close()
			panelCfg := config.PanelConfig{URL: server.URL, MachineID: 3}
			client := panel.NewClient(panelCfg)
			handshake := func(c *panel.Client) {
				t.Helper()
				if _, err := c.Handshake(); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{
				Panel:  config.PanelConfig{URL: server.URL, MachineID: 3, NodeID: 7},
				Kernel: config.KernelConfig{ConfigDir: t.TempDir()},
			}
			keep := name == "machine node rebuild" || name == "new process keeps valid queue"
			if keep {
				phase.Store(1)
				handshake(client)
				phase.Store(2)
			}
			handshake(client)
			sink := controlplane.NewMachinePanelControlPlane(client.ForNode(7), nil, nil)
			s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
			s.initializeAccessLog()
			s.accessLog.Add(9, "cached.example.test", "", 443)
			first, _ := s.accessLog.Next()
			if err := s.accessLog.Save(s.accessLogPath()); err != nil {
				t.Fatal(err)
			}
			if !keep {
				phase.Store(1)
				if name == "crash before cleanup" {
					handshake(client)
				}
				phase.Store(2)
				if name == "crash before cleanup" {
					handshake(client)
				}
			}
			if name != "machine node rebuild" {
				client = panel.NewClient(panelCfg)
				handshake(client)
			}
			sink = controlplane.NewMachinePanelControlPlane(client.ForNode(7), nil, nil)
			restarted := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}, sink: sink}
			restarted.initializeAccessLog()
			got, _ := restarted.accessLog.Next()
			if keep && (got == nil || got.ID != first.ID) {
				t.Fatal("有效批次没有按原编号恢复")
			}
			if !keep && got != nil {
				t.Fatal("停用前的旧缓存跨重启恢复")
			}
			restarted.flushAccessLog(t.Context(), sink)
			want := int32(0)
			if keep {
				want = 1
			}
			if reports.Load() != want {
				t.Fatal("旧批次被补报，或有效批次未上报")
			}
		})
	}
}

func TestAccessLogFlushRetryRestoresExactBatch(t *testing.T) {
	cfg := &config.Config{Panel: config.PanelConfig{URL: "http://127.0.0.1", NodeID: 7}, Kernel: config.KernelConfig{ConfigDir: t.TempDir()}}
	s := &Service{cfg: cfg, accessLog: &accesslog.Recorder{NodeID: 7}}
	s.accessLog.Enable(true)
	s.accessLog.AddForNode(11, 9, "example.test", "", 443)
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
	if len(sink.records) != 2 || sink.records[0].NodeID != 11 || sink.records[1] != sink.records[0] {
		t.Fatal("失败恢复后实际落地或记录内容改变")
	}
	if batch, _ := restored.accessLog.Next(); batch != nil {
		t.Fatal("确认后记录仍未清理")
	}
}
