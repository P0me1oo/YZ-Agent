package service

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

type shutdownReportPlane struct {
	controlplane.ControlPlane
	mu      sync.Mutex
	reports []controlplane.ReportPayload
	onSend  func(controlplane.ReportPayload) error
}

func (p *shutdownReportPlane) SupportsReporting() bool { return true }
func (p *shutdownReportPlane) Metrics() controlplane.APIMetrics {
	return controlplane.APIMetrics{}
}
func (p *shutdownReportPlane) Report(payload controlplane.ReportPayload) error {
	p.mu.Lock()
	p.reports = append(p.reports, payload)
	p.mu.Unlock()
	if p.onSend != nil {
		return p.onSend(payload)
	}
	return nil
}
func (p *shutdownReportPlane) snapshot() []controlplane.ReportPayload {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]controlplane.ReportPayload(nil), p.reports...)
}

type shutdownTrafficKernel struct {
	*fakeKernel
	traffic map[int][2]int64
}

func (k *shutdownTrafficKernel) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	return k.traffic, nil, 0, nil
}

func newShutdownReportService() (*Service, *shutdownReportPlane) {
	s := newTestService(&fakeKernel{})
	s.tracker = tracker.New()
	cp := &shutdownReportPlane{}
	s.source, s.sink = cp, cp
	return s, cp
}

func recordShutdownTraffic(s *Service, value int64) {
	s.tracker.Process(map[int][2]int64{1: {value, value}}, nil, 0)
	s.tracker.ProcessRelay(map[int][2]int64{2: {value, value}})
	s.tracker.ProcessRelayUser(map[int]map[int][2]int64{1: {2: {value, value}}})
}

func TestShutdownRetriesFrozenReportThenFlushesNewTraffic(t *testing.T) {
	s, cp := newShutdownReportService()
	recordShutdownTraffic(s, 100)
	failed := s.takeReportBatch()
	s.rememberFailedReport(failed)
	recordShutdownTraffic(s, 200)
	s.pushReportSync()
	reports := cp.snapshot()
	if len(reports) != 2 {
		t.Fatalf("关闭时应先重试旧报告再发送新增流量，实际发送 %d 批", len(reports))
	}
	if !reflect.DeepEqual(reports[0], failed.payload) || reports[0].ReportID == reports[1].ReportID {
		t.Fatal("重试批次内容发生改变或新增流量复用了旧 report_id")
	}
	for _, report := range reports {
		if report.Traffic[1] != [2]int64{100, 100} || report.RelayTraffic[2] != [2]int64{100, 100} || report.RelayUserTraffic[1][2] != [2]int64{100, 100} {
			t.Fatal("关闭报告的用户或中转流量未完整分批结算")
		}
	}
	if len(s.tracker.FlushTraffic())+len(s.tracker.FlushRelayTraffic())+len(s.tracker.FlushRelayUserTraffic()) != 0 || s.retryReport != nil {
		t.Fatal("成功关闭后仍有未上报流量")
	}
}

func TestShutdownSamplesFinalKernelTraffic(t *testing.T) {
	s, cp := newShutdownReportService()
	s.kernel = &shutdownTrafficKernel{fakeKernel: &fakeKernel{}, traffic: map[int][2]int64{1: {200, 200}}}
	s.tracker.Process(map[int][2]int64{1: {100, 100}}, nil, 0)
	s.pushReportSync()
	reports := cp.snapshot()
	if len(reports) != 1 || reports[0].Traffic[1] != [2]int64{200, 200} {
		t.Fatal("未采集内核停止前最后一个采样周期的流量")
	}
}

func TestTrackStoppedKernelCollectsFinalTrafficWithoutRestart(t *testing.T) {
	s, _ := newShutdownReportService()
	k := &shutdownTrafficKernel{fakeKernel: &fakeKernel{}, traffic: map[int][2]int64{1: {200, 200}}}
	s.kernel = k
	s.tracker.Process(map[int][2]int64{1: {100, 100}}, nil, 0)
	s.trackAndEnforce(context.Background())
	if got := s.tracker.FlushTraffic()[1]; got != [2]int64{200, 200} || k.startCalls != 0 {
		t.Fatalf("停止节点的尾部流量没有结算或空配置被重启: traffic=%v, starts=%d", got, k.startCalls)
	}
}

func TestShutdownKeepsFailedBatchAndPendingTraffic(t *testing.T) {
	useFastFinalRetries(t)
	s, cp := newShutdownReportService()
	recordShutdownTraffic(s, 100)
	failed := s.takeReportBatch()
	s.rememberFailedReport(failed)
	recordShutdownTraffic(s, 200)
	cp.onSend = func(controlplane.ReportPayload) error { return errors.New("测试网络故障") }
	s.pushReportSync()
	if len(cp.snapshot()) != 1+len(finalReportRetryDelays) || s.retryReport != failed {
		t.Fatal("失败的关闭重试未保留原批次")
	}
	if s.tracker.FlushTraffic()[1] != [2]int64{100, 100} || s.tracker.FlushRelayTraffic()[2] != [2]int64{100, 100} || s.tracker.FlushRelayUserTraffic()[1][2] != [2]int64{100, 100} {
		t.Fatal("旧报告仍失败时不应消费后续流量")
	}
}

func TestShutdownWaitsForInFlightReport(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failFirst], func(t *testing.T) {
			s, cp := newShutdownReportService()
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			var calls int
			cp.onSend = func(controlplane.ReportPayload) error {
				cp.mu.Lock()
				calls++
				first := calls == 1
				cp.mu.Unlock()
				if first {
					close(started)
					<-release
					if failFirst {
						return errors.New("测试在途报告失败")
					}
				}
				return nil
			}
			recordShutdownTraffic(s, 100)
			s.pushReportAsync()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("异步报告未启动")
			}
			recordShutdownTraffic(s, 200)
			done := make(chan struct{})
			go func() { s.pushReportSync(); close(done) }()
			select {
			case <-done:
				t.Fatal("关闭未等待在途报告")
			case <-time.After(50 * time.Millisecond):
			}
			if len(cp.snapshot()) != 1 {
				t.Error("关闭报告与异步报告发生重叠")
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("在途报告结束后关闭仍未完成")
			}
			reports := cp.snapshot()
			want := 2
			if failFirst {
				want = 3
			}
			if len(reports) != want {
				t.Fatalf("实际发送 %d 批，预期 %d 批", len(reports), want)
			}
			if failFirst && !reflect.DeepEqual(reports[0], reports[1]) {
				t.Fatal("在途失败后的重试更改了 report_id 或批次内容")
			}
			if reports[want-1].Traffic[1] != [2]int64{100, 100} || reports[want-1].ReportID == reports[0].ReportID {
				t.Fatal("在途报告期间产生的流量未单独发送")
			}
		})
	}
}

func TestShutdownBudgetIncludesInFlightReport(t *testing.T) {
	oldBudget := finalReportBudget
	finalReportBudget = 40 * time.Millisecond
	t.Cleanup(func() { finalReportBudget = oldBudget })
	s, cp := newShutdownReportService()
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	cp.onSend = func(controlplane.ReportPayload) error {
		close(started)
		<-release
		return nil
	}
	recordShutdownTraffic(s, 100)
	s.pushReportAsync()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("在途报告没有开始")
	}
	begin := time.Now()
	s.pushReportSync()
	if elapsed := time.Since(begin); elapsed > 300*time.Millisecond {
		t.Fatalf("等待在途报告超过退出预算: %s", elapsed)
	}
	if len(cp.snapshot()) != 1 {
		t.Fatal("到期后不应并行发送新的报告")
	}
}
