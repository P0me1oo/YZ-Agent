package panel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLimitEventJSONIncludesDeviceIPsOnlyWhenPresent(t *testing.T) {
	device, err := json.Marshal(LimitEvent{UserID: 1, Kind: "device", Limit: 2, Observed: 2, Count: 5, IPs: []string{"8.8.8.8"}})
	if err != nil {
		t.Fatalf("marshal device event: %v", err)
	}
	if !strings.Contains(string(device), `"ips":["8.8.8.8"]`) {
		t.Fatalf("设备超限事件应带被拒来源：%s", device)
	}

	conn, err := json.Marshal(LimitEvent{UserID: 1, Kind: "conn", Limit: 8, Observed: 8, Count: 1})
	if err != nil {
		t.Fatalf("marshal conn event: %v", err)
	}
	if strings.Contains(string(conn), `"ips"`) {
		t.Fatalf("连接数事件不应多出来源字段，保持旧面板看到的格式：%s", conn)
	}
}
