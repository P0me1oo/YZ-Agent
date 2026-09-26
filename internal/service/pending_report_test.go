package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

func pendingTestConfig(dir string, nodeID int) *config.Config {
	return &config.Config{
		Panel:  config.PanelConfig{URL: "https://panel.example/", NodeID: nodeID},
		Kernel: config.KernelConfig{Type: "xray", ConfigDir: dir},
	}
}

// newPersistentReportService 创建带落盘存储的测试服务，同一目录模拟同一节点的前后两次运行。
func newPersistentReportService(t *testing.T, dir string) (*Service, *shutdownReportPlane) {
	t.Helper()
	s, cp := newShutdownReportService()
	s.cfg = pendingTestConfig(dir, 7)
	s.pendingStore = newPendingReportStore(s.cfg)
	if s.pendingStore == nil {
		t.Fatal("面板节点应启用落盘存储")
	}
	return s, cp
}

func useFastFinalRetries(t *testing.T) {
	t.Helper()
	oldDelays, oldBudget := finalReportRetryDelays, finalReportBudget
	finalReportRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	finalReportBudget = 5 * time.Second
	t.Cleanup(func() { finalReportRetryDelays, finalReportBudget = oldDelays, oldBudget })
}

func pendingFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "pending-report-*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func sumReportedTraffic(reports []controlplane.ReportPayload) [2]int64 {
	var total [2]int64
	seen := map[string]bool{}
	for _, report := range reports {
		// 面板按 report_id 去重，同一编号只计一次。
		if seen[report.ReportID] {
			continue
		}
		seen[report.ReportID] = true
		total[0] += report.Traffic[1][0]
		total[1] += report.Traffic[1][1]
	}
	return total
}

func TestPendingReportStoreRoundTripAndCleanup(t *testing.T) {
	dir := t.TempDir()
	store := newPendingReportStore(pendingTestConfig(dir, 7))
	batch := &pendingBatch{ID: "boot-1", pendingTraffic: pendingTraffic{
		Traffic:   map[int][2]int64{1: {10, 20}},
		Relay:     map[int][2]int64{2: {3, 4}},
		RelayUser: map[int]map[int][2]int64{1: {2: {5, 6}}},
	}}
	pending := pendingTraffic{Traffic: map[int][2]int64{1: {7, 8}}}
	if err := store.save(batch, pending); err != nil {
		t.Fatal(err)
	}
	gotBatch, gotPending, err := store.load()
	if err != nil || gotBatch == nil || gotBatch.ID != "boot-1" || gotBatch.Traffic[1] != [2]int64{10, 20} ||
		gotBatch.Relay[2] != [2]int64{3, 4} || gotBatch.RelayUser[1][2] != [2]int64{5, 6} || gotPending.Traffic[1] != [2]int64{7, 8} {
		t.Fatalf("round trip mismatch: batch=%+v pending=%+v err=%v", gotBatch, gotPending, err)
	}
	if err := store.save(nil, pendingTraffic{}); err != nil {
		t.Fatal(err)
	}
	if files := pendingFiles(t, dir); len(files) != 0 {
		t.Fatalf("没有待确认数据时应删除文件，实际剩余 %v", files)
	}
}

func TestPendingReportStoreIgnoresOtherNodesAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	first := newPendingReportStore(pendingTestConfig(dir, 7))
	other := newPendingReportStore(pendingTestConfig(dir, 8))
	if first.path == other.path {
		t.Fatal("不同节点必须使用不同的保存文件")
	}
	if err := first.save(nil, pendingTraffic{Traffic: map[int][2]int64{1: {1, 1}}}); err != nil {
		t.Fatal(err)
	}
	if batch, pending, err := other.load(); err != nil || batch != nil || !pending.empty() {
		t.Fatal("改绑到其他节点后不应读到原节点的数据")
	}

	if err := os.WriteFile(first.path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.load(); err == nil {
		t.Fatal("损坏的文件应报告错误")
	}
	files := pendingFiles(t, dir)
	if len(files) != 1 || !strings.Contains(files[0], ".invalid-") {
		t.Fatalf("损坏的文件应改名保留，实际 %v", files)
	}
	if batch, pending, err := first.load(); err != nil || batch != nil || !pending.empty() {
		t.Fatal("改名后应按空数据继续运行")
	}
}

// 批次发送成功后进程立即退出、没来得及删除文件：重启后原样重发，面板按编号去重。
func TestRestartResendsSavedBatchWithOriginalID(t *testing.T) {
	dir := t.TempDir()
	first, _ := newPersistentReportService(t, dir)
	recordShutdownTraffic(first, 100)
	sent := first.takeReportBatch()
	recordShutdownTraffic(first, 130) // 发出批次后又产生的 30，尚未组成批次
	_ = first.persistPending()        // 模拟下一次采样，在途批次和新增流量一起保存

	second, cp := newPersistentReportService(t, dir)
	second.restorePendingReports()
	resent := second.takeReportBatch()
	if resent.id != sent.id || resent.payload.ReportID != sent.id || resent.payload.Traffic[1] != [2]int64{100, 100} {
		t.Fatalf("重发批次应沿用原编号和流量: got id=%s traffic=%v", resent.id, resent.payload.Traffic)
	}
	if resent.payload.Alive != nil || resent.payload.Online != nil || resent.payload.Metrics == nil {
		t.Fatal("恢复的批次不应重发旧设备快照，但应带上当前状态")
	}
	second.pushReportSync()
	reports := cp.snapshot()
	if len(reports) != 2 || reports[1].ReportID == sent.id || reports[1].Traffic[1] != [2]int64{30, 30} {
		t.Fatalf("尚未组成批次的流量应以新编号单独上报: %+v", reports)
	}
	if files := pendingFiles(t, dir); len(files) != 0 {
		t.Fatalf("全部确认后应删除保存文件，实际 %v", files)
	}
}

// 面板在退出期间不可用：退出前保存全部数据，下次启动补报，总量不丢不重。
func TestShutdownSavesTrafficWhenPanelUnavailable(t *testing.T) {
	useFastFinalRetries(t)
	dir := t.TempDir()
	first, down := newPersistentReportService(t, dir)
	down.onSend = func(controlplane.ReportPayload) error { return errors.New("面板维护中") }
	recordShutdownTraffic(first, 100)
	failed := first.takeReportBatch()
	if err := first.sink.Report(failed.payload); err == nil {
		t.Fatal("测试面板应处于故障状态")
	}
	first.rememberFailedReport(failed)
	recordShutdownTraffic(first, 250)
	first.pushReportSync()
	if got := len(down.snapshot()); got != 1+len(finalReportRetryDelays)+1 {
		t.Fatalf("退出时应在时限内重试，实际发送 %d 次", got)
	}
	if files := pendingFiles(t, dir); len(files) != 1 {
		t.Fatalf("面板不可用时应保留保存文件，实际 %v", files)
	}

	second, up := newPersistentReportService(t, dir)
	second.tracker = tracker.New()
	second.restorePendingReports()
	second.pushReportSync()
	reports := append(down.snapshot(), up.snapshot()...)
	if got := sumReportedTraffic(reports); got != [2]int64{250, 250} {
		t.Fatalf("按编号去重后的流量总量应为 250，实际 %v", got)
	}
	if up.snapshot()[0].ReportID != failed.id {
		t.Fatal("重启后应先重发原批次")
	}
	if files := pendingFiles(t, dir); len(files) != 0 {
		t.Fatalf("补报成功后应删除保存文件，实际 %v", files)
	}
}

func TestFinalReportRetriesUntilPanelRecovers(t *testing.T) {
	useFastFinalRetries(t)
	s, cp := newShutdownReportService()
	failures := 2
	cp.onSend = func(controlplane.ReportPayload) error {
		if failures > 0 {
			failures--
			return errors.New("面板正在重启")
		}
		return nil
	}
	recordShutdownTraffic(s, 100)
	s.pushReportSync()
	reports := cp.snapshot()
	if len(reports) != 3 || reports[2].ReportID != reports[0].ReportID || s.retryReport != nil {
		t.Fatalf("面板恢复后应以同一编号补报成功，实际 %d 次", len(reports))
	}
}

func TestStandaloneDisablesPendingStore(t *testing.T) {
	cfg := pendingTestConfig(t.TempDir(), 7)
	cfg.Standalone = &config.StandaloneConfig{Enabled: true}
	if newPendingReportStore(cfg) != nil {
		t.Fatal("独立运行不向面板上报，不应创建保存文件")
	}
	cfg = pendingTestConfig("", 7)
	if newPendingReportStore(cfg) != nil {
		t.Fatal("没有配置目录时不应创建保存文件")
	}
}

// 面板请求未结束时继续采样，磁盘必须同时包含在途批次和之后产生的流量。
func TestSamplingPersistsInFlightBatchAndNewTraffic(t *testing.T) {
	dir := t.TempDir()
	s, _ := newPersistentReportService(t, dir)
	recordShutdownTraffic(s, 100)
	batch := s.takeReportBatch()
	s.kernel = &shutdownTrafficKernel{fakeKernel: &fakeKernel{}, traffic: map[int][2]int64{1: {150, 150}}}
	if _, _, err := s.collectTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored, _ := newPersistentReportService(t, dir)
	restored.restorePendingReports()
	if restored.retryReport == nil || restored.retryReport.id != batch.id || restored.retryReport.payload.Traffic[1] != [2]int64{100, 100} {
		t.Fatal("采样覆盖了尚未确认的批次")
	}
	if got := restored.tracker.FlushTraffic()[1]; got != [2]int64{50, 50} {
		t.Fatalf("采样增量未保存: %v", got)
	}
}

func TestDiskFailureKeepsReportIDAndPreventsSending(t *testing.T) {
	s, cp := newPersistentReportService(t, t.TempDir())
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.pendingStore.path = filepath.Join(blocked, "report.json")
	recordShutdownTraffic(s, 100)
	batch := s.takeReportBatch()
	if err := s.sendReport(batch); err == nil || len(cp.snapshot()) != 0 {
		t.Fatal("磁盘失败时不应发送无法恢复的批次")
	}
	if s.takeReportBatch().id != batch.id {
		t.Fatal("磁盘失败不能更换批次编号")
	}
	s.pendingStore.path = filepath.Join(t.TempDir(), "report.json")
	if err := s.sendReport(batch); err != nil {
		t.Fatal(err)
	}
	s.forgetCompletedReport(batch)
	if s.retryReport != nil || len(cp.snapshot()) != 1 || cp.snapshot()[0].ReportID != batch.id {
		t.Fatal("磁盘恢复后应使用原编号完成上报")
	}
}
