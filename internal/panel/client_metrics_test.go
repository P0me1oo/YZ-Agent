package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

// 一次成功的上报只计一次成功，失败只计一次失败。
func TestClientCountsEachRequestOnce(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()
	c := NewClient(config.PanelConfig{URL: server.URL, Token: "test", NodeID: 1})

	if err := c.PushStatus(0, [2]uint64{}, [2]uint64{}, [2]uint64{}); err != nil {
		t.Fatal(err)
	}
	if m := c.SnapshotMetrics(); m.Success != 1 || m.Failure != 0 {
		t.Fatalf("成功请求计数 = %+v，应为 1 次成功", m)
	}
	status = http.StatusInternalServerError
	if err := c.PushStatus(0, [2]uint64{}, [2]uint64{}, [2]uint64{}); err == nil {
		t.Fatal("面板返回 500 时应报错")
	}
	if m := c.SnapshotMetrics(); m.Success != 1 || m.Failure != 1 {
		t.Fatalf("失败请求计数 = %+v，应为 1 次失败", m)
	}
}
