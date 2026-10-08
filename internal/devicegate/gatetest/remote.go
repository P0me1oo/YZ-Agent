package gatetest

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

// Remote 只模拟接口边界，具体替换决策由各用例明确给出。
type Remote struct {
	Calls atomic.Int64
	Admit func(context.Context, devicegate.AdmissionRequest) (devicegate.AdmissionReply, error)
	Sync  func(context.Context, devicegate.Snapshot) (devicegate.SyncReply, error)
}

func (*Remote) DeviceHandoverSupported() bool { return true }
func (*Remote) BeginDeviceSession(_ context.Context, run string) (devicegate.BeginReply, error) {
	return devicegate.BeginReply{Version: 1, Run: run}, nil
}
func (r *Remote) AdmitDeviceSource(ctx context.Context, req devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
	n := r.Calls.Add(1)
	if r.Admit != nil {
		return r.Admit(ctx, req)
	}
	return devicegate.AdmissionReply{Status: "allowed", Lease: fmt.Sprintf("%032x", n)}, nil
}
func (r *Remote) SyncDeviceSession(ctx context.Context, s devicegate.Snapshot) (devicegate.SyncReply, error) {
	if r.Sync != nil {
		return r.Sync(ctx, s)
	}
	return devicegate.SyncReply{Run: s.Run, Sequence: s.Sequence}, nil
}

func Manager(t *testing.T, remote *Remote) *devicegate.Manager {
	t.Helper()
	m := devicegate.New(remote, nil)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func Source(t *testing.T, manager *devicegate.Manager, user int, ip string) devicegate.Source {
	t.Helper()
	for _, source := range manager.Snapshot().Sources {
		if source.UserID == user && source.IP == ip {
			return source
		}
	}
	t.Fatal("未找到已登记的测试来源")
	return devicegate.Source{}
}
