package machine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/service"
)

func TestSlowNodePreparationDoesNotDelayOtherNodesAndPrefetchIsReused(t *testing.T) {
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var mu sync.Mutex
	requests := make(map[int]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/server/config" {
			id, _ := strconv.Atoi(r.URL.Query().Get("node_id"))
			mu.Lock()
			requests[id]++
			mu.Unlock()
			if id == 1 {
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			w.Header().Set("ETag", fmt.Sprintf(`"node-%d"`, id))
			fmt.Fprintf(w, `{"protocol":"vless","server_port":%d}`, 10000+id)
		} else if r.URL.Path == "/api/v2/server/user" {
			fmt.Fprint(w, `{"users":[]}`)
		} else {
			fmt.Fprint(w, `{"settings":{"push_interval":60,"pull_interval":60},"websocket":{"enabled":false}}`)
		}
	}))
	defer server.Close()
	cfg := &config.Config{InstanceID: "startup-test", Panel: config.PanelConfig{URL: server.URL}, Machine: &config.MachineConfig{MachineID: 1, Token: "test"}, Kernel: config.KernelConfig{Type: "xray", ConfigDir: t.TempDir()}, Node: config.NodeConfig{TrackInterval: 60, DeviceReportInterval: 60}, WS: config.WSConfig{DiscoveryInterval: 60}}
	o := New(cfg)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		o.startNodes(ctx, []panel.MachineNode{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}, {ID: 5}})
		close(done)
	}()
	<-entered
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		o.statusMu.Lock()
		ready := o.nodeStatuses[2] == service.RuntimeRunning && o.nodeStatuses[5] == service.RuntimeRunning
		o.statusMu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			unblock()
			<-done
			o.stopAll()
			t.Fatal("慢节点串行阻塞其他节点")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	unblock()
	<-done
	cancel()
	o.stopAll()
	mu.Lock()
	defer mu.Unlock()
	for id, count := range requests {
		if count != 1 {
			t.Fatalf("节点 %d 预取后又拉取 %d 次", id, count)
		}
	}
}
