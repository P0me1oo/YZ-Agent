package xray

import (
	"sync"
	"testing"
)

func TestDeviceLimitChangePreservesActiveSources(t *testing.T) {
	for _, limits := range [][]int{{0, 1}, {1, 0, 1}, {2, 0, 1}} {
		d := newTestDispatcher()
		email := userEmail(15)
		d.UpdateLimits(map[string]int{email: 15}, map[string]int{email: limits[0]}, nil)
		if d.checkDeviceLimit(email, "8.8.8.8", true) {
			t.Fatal("首个测试来源意外被拒")
		}
		for _, limit := range limits[1:] {
			d.UpdateLimits(map[string]int{email: 15}, map[string]int{email: limit}, nil)
		}
		if !d.checkDeviceLimit(email, "1.1.1.1", true) {
			t.Errorf("上限变化 %v 后，原来源仍在线，第二个来源却被允许", limits)
		}
	}
}

func TestDeviceLimitChangeReleasesEveryClosedSource(t *testing.T) {
	for _, keepOld := range []bool{false, true} {
		d := newTestDispatcher()
		email := userEmail(15)
		d.UpdateLimits(map[string]int{email: 15}, nil, nil)
		d.checkDeviceLimit(email, "8.8.8.8", true)
		if !keepOld {
			d.delConn(email, "8.8.8.8")
		}
		d.UpdateLimits(map[string]int{email: 15}, map[string]int{email: 2}, nil)
		if d.checkDeviceLimit(email, "1.1.1.1", true) {
			t.Fatal("名额未满时第二个测试来源意外被拒")
		}
		d.delConn(email, "1.1.1.1")
		if keepOld {
			d.delConn(email, "8.8.8.8")
		}
		states, _ := d.GetConnectionState()
		counts, _ := d.ConnectionSnapshot()
		if len(states[15]) != 0 || counts[15] != 0 {
			t.Errorf("全部连接关闭后仍有设备或连接，保留旧连接=%v，设备=%d，连接=%d", keepOld, len(states[15]), counts[15])
		}
		if d.checkDeviceLimit(email, "9.9.9.9", true) {
			t.Error("关闭全部连接后没有归还设备名额")
		}
	}
}

func TestDeviceLimitChangeConcurrentConnectionsReturnToZero(t *testing.T) {
	d := newTestDispatcher()
	email := userEmail(15)
	d.UpdateLimits(map[string]int{email: 15}, nil, nil)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := 0; i < 500; i++ {
				if !d.checkDeviceLimit(email, "8.8.8.8", true) {
					d.delConn(email, "8.8.8.8")
				}
			}
		}()
	}
	for i := 0; i < 500; i++ {
		d.UpdateLimits(map[string]int{email: 15}, map[string]int{email: i % 2}, nil)
		d.GetConnectionState()
		d.ConnectionSnapshot()
	}
	workers.Wait()
	states, _ := d.GetConnectionState()
	counts, _ := d.ConnectionSnapshot()
	if len(states[15]) != 0 || counts[15] != 0 {
		t.Fatalf("并发变更上限和关闭连接后仍有残留：设备=%d，连接=%d", len(states[15]), counts[15])
	}
}
