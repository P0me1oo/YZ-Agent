package xray

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestDeviceExcludeListsDoNotCrossInstances(t *testing.T) {
	first, second := New(config.KernelConfig{}), New(config.KernelConfig{})
	first.SetDeviceIPExclude([]string{"203.0.114.7"})
	second.SetDeviceIPExclude([]string{"203.0.114.8"})
	if first.deviceFilter.CountKey("203.0.114.7") != "" || first.deviceFilter.CountKey("203.0.114.8") == "" {
		t.Fatal("第一个实例的设备排除名单不正确")
	}
	if second.deviceFilter.CountKey("203.0.114.8") != "" || second.deviceFilter.CountKey("203.0.114.7") == "" {
		t.Fatal("第二个实例的设备排除名单被覆盖")
	}
	first.SetDeviceIPExclude(nil)
	if second.deviceFilter.CountKey("203.0.114.8") != "" {
		t.Fatal("清空第一个实例的名单影响了第二个实例")
	}
}
