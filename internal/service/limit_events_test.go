package service

import (
	"slices"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
)

func TestCollectLimitEventsIncludesDeviceEvents(t *testing.T) {
	l := limiter.New()
	l.UpdateUsers([]model.UserSpec{
		{ID: 1, UUID: "a", DeviceLimit: 2, ConnLimit: 8},
		{ID: 2, UUID: "b", DeviceLimit: 1},
	})
	l.ReportDeviceLimited(2, 1, 1, "8.8.8.8")
	l.ReportLimited(1, model.ConnLimitKindConcurrent, 8, 8)
	l.ReportDeviceLimited(1, 2, 2, "8.8.4.4")
	l.ReportDeviceLimited(1, 2, 2, "8.8.4.4")

	s := &Service{limiter: l}
	events := s.collectLimitEvents()
	if len(events) != 3 {
		t.Fatalf("事件数 = %d，期望 3：%#v", len(events), events)
	}
	// 按用户、类型排序，便于面板日志和重试比对。
	if events[0].UserID != 1 || events[0].Kind != model.ConnLimitKindConcurrent || events[0].IPs != nil {
		t.Fatalf("events[0] = %#v", events[0])
	}
	device := events[1]
	if device.UserID != 1 || device.Kind != model.ConnLimitKindDevice || device.Limit != 2 ||
		device.Observed != 2 || device.Count != 2 || !slices.Equal(device.IPs, []string{"8.8.4.4"}) {
		t.Fatalf("events[1] = %#v", device)
	}
	if events[2].UserID != 2 || events[2].Kind != model.ConnLimitKindDevice || events[2].Count != 1 {
		t.Fatalf("events[2] = %#v", events[2])
	}
}

func TestCollectLimitEventsPreservesSnapshotsForEveryKind(t *testing.T) {
	l := limiter.New()
	l.UpdateUsers([]model.UserSpec{{ID: 1, DeviceLimit: 2, ConnLimit: 8, ConnRateLimit: 1}})
	for _, kind := range []string{model.ConnLimitKindConcurrent, model.ConnLimitKindRate, model.ConnLimitKindDevice} {
		l.ReportLimitedSnapshot(1, kind, 0, 2, "203.0.113.1", func() *model.LimitSnapshot {
			return model.NewLimitSnapshot("203.0.113.1", []string{"198.51.100.1"}, true)
		})
	}
	s := &Service{limiter: l}
	events := s.collectLimitEvents()
	if len(events) != 3 {
		t.Fatalf("期望三种事件，实际 %d", len(events))
	}
	for _, event := range events {
		if event.Snapshot == nil || event.Snapshot.SourceIP != "203.0.113.1" || !slices.Equal(event.Snapshot.ActiveIPs, []string{"198.51.100.1"}) {
			t.Fatalf("事件快照丢失：%+v", event)
		}
	}
	if len(s.collectLimitEvents()) != 0 {
		t.Fatal("重复采集重复发送")
	}
}
