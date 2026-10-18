package panel

import (
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessLogProtocolAuthCapabilityAndReceipt(t *testing.T) {
	mode := "enabled"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if data["machine_id"] != float64(3) || data["node_id"] != float64(7) || data["token"] != "access-test-only" {
			t.Error("请求认证身份错误")
		}
		if r.URL.Path == "/api/v2/server/handshake" {
			version := 2
			if mode == "legacy" {
				version = 0
			}
			if mode == "entry-only" {
				version = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_log": map[string]int{"version": version}})
			return
		}
		if r.URL.Path != "/api/v2/server/access-log" {
			t.Error("上报路径错误")
		}
		records := data["records"].([]any)
		if records[0].(map[string]any)["node_id"] != float64(9) {
			t.Error("实际落地归属丢失或覆盖了入口认证")
		}
		if mode == "fail" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("不要回显访问详情"))
			return
		}
		id := data["batch_id"]
		if mode == "wrong" {
			id = "wrong"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"batch_id": id}})
	}))
	defer server.Close()
	client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 3, Token: "access-test-only"}).ForNode(7)
	batch := &accesslog.Batch{ID: strings.Repeat("a", 32), Records: []accesslog.Record{{NodeID: 9, UserID: 11, Destination: "example.test", Port: 443, ConnectedAt: time.Now().Unix()}}}
	if err := client.ReportAccessLog(t.Context(), batch); err == nil {
		t.Fatal("握手前不得上报")
	}
	if _, err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := client.ReportAccessLog(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	mode = "wrong"
	if err := client.ReportAccessLog(t.Context(), batch); err == nil {
		t.Fatal("错误批次被确认")
	}
	mode = "fail"
	if err := client.ReportAccessLog(t.Context(), batch); err == nil || strings.Contains(err.Error(), "详情") {
		t.Fatal("失败响应处理错误")
	}
	mode = "legacy"
	if _, err := client.Handshake(); err != nil || client.AccessLogEnabled() {
		t.Fatal("旧面板能力回退失败")
	}
	mode = "entry-only"
	if _, err := client.Handshake(); err != nil || client.AccessLogEnabled() {
		t.Fatal("只支持入口归属的旧面板不得启用新版采集")
	}
}
