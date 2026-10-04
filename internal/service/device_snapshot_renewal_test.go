package service

import (
	"context"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type deviceSnapshotKernel struct {
	*fakeKernel
	devices map[int][]string
	updates int
	clears  int
}

func (k *deviceSnapshotKernel) UpdateGlobalDevices(users map[int][]string) {
	k.devices = users
	k.updates++
}

func (k *deviceSnapshotKernel) ClearGlobalDevices() {
	k.devices = nil
	k.clears++
}

func TestUnchangedDeviceSnapshotRenewalSurvivesExpiryWindow(t *testing.T) {
	k := &deviceSnapshotKernel{fakeKernel: &fakeKernel{}}
	s := &Service{kernel: k}
	version := panel.StateVersion{Epoch: "device-test", Sequence: 1}
	users := map[int][]string{15: {"8.8.8.8"}}
	s.applyDeviceSnapshot(version, users)
	clock := time.Now()
	s.lastDeviceSync = clock
	// 模拟面板每十秒发送相同名单，连续运行八十秒。
	for i := 0; i < 8; i++ {
		clock = clock.Add(10 * time.Second)
		s.expireDeviceSnapshot(clock)
		if k.clears != 0 || len(k.devices[15]) != 1 {
			t.Fatal("正常续期期间丢失了其他节点的设备")
		}
		previous := time.Now().Add(-10 * time.Second)
		s.lastDeviceSync = previous
		s.applyDeviceSnapshot(version, users)
		if !s.lastDeviceSync.After(previous) {
			t.Fatal("同一版本的有效续期没有刷新接收时间")
		}
		s.lastDeviceSync = clock
	}
	if k.updates != 9 {
		t.Fatal("同一版本的有效续期被丢弃")
	}
	s.expireDeviceSnapshot(clock.Add(36 * time.Second))
	s.expireDeviceSnapshot(clock.Add(40 * time.Second))
	if k.clears != 1 || len(k.devices) != 0 {
		t.Fatal("真正失联后应清除跨节点设备，且只清除一次")
	}
	s.applyDeviceSnapshot(version, users)
	if len(k.devices[15]) != 1 || s.lastDeviceSync.IsZero() {
		t.Fatal("过期后收到相同有效版本应恢复设备状态")
	}
}

func TestDeviceRenewalReachesBothMachineNodeMailboxes(t *testing.T) {
	version := panel.StateVersion{Epoch: "machine-device-test", Sequence: 1}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1"} {
		k := &deviceSnapshotKernel{fakeKernel: &fakeKernel{}}
		s := &Service{kernel: k, machineMailbox: controlplane.NewNodeMailbox()}
		s.machineMailbox.MarkReady()
		for i := 0; i < 8; i++ {
			previous := time.Now().Add(-30 * time.Second)
			s.lastDeviceSync = previous
			s.machineMailbox.Apply(controlplane.Event{
				Type: controlplane.EventSyncDevices, Version: version,
				DeviceUsers: map[int][]string{15: {ip}},
			})
			s.drainMachineMailbox(context.Background())
			if !s.lastDeviceSync.After(previous) || len(k.devices[15]) != 1 || k.devices[15][0] != ip {
				t.Fatal("共享机器连接丢失同版本续期或混淆节点设备名单")
			}
		}
		if k.updates != 8 {
			t.Fatal("共享机器连接合并消息时丢弃了有效续期")
		}
	}
}

func TestOldDeviceSnapshotCannotRenewAndEmptySnapshotClears(t *testing.T) {
	k := &deviceSnapshotKernel{fakeKernel: &fakeKernel{}}
	s := &Service{kernel: k}
	version := panel.StateVersion{Epoch: "device-test", Sequence: 2}
	s.applyDeviceSnapshot(version, map[int][]string{15: {"8.8.8.8"}})
	previous := time.Now().Add(-30 * time.Second)
	s.lastDeviceSync = previous
	s.applyDeviceSnapshot(panel.StateVersion{Epoch: version.Epoch, Sequence: 1}, map[int][]string{})
	s.applyDeviceSnapshot(version, nil)
	if !s.lastDeviceSync.Equal(previous) || len(k.devices[15]) != 1 {
		t.Fatal("旧版本或缺失设备名单不应覆盖或续期")
	}
	version.Sequence++
	s.applyDeviceSnapshot(version, map[int][]string{})
	if !s.lastDeviceSync.After(previous) || len(k.devices) != 0 {
		t.Fatal("有效空名单应立即清空设备并更新有效期")
	}
}
