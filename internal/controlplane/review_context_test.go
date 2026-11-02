package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestLegacyPollingHonorsCancellationBeforeAndDuringConfigRequest(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		t.Run(phase, func(t *testing.T) {
			var calls atomic.Int32
			entered, canceled := make(chan struct{}, 1), make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				entered <- struct{}{}
				<-r.Context().Done()
				canceled <- struct{}{}
			}))
			defer server.Close()
			for name, cp := range snapshotControlPlanes(server.URL) {
				t.Run(name, func(t *testing.T) {
					calls.Store(0)
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					if phase == "before" {
						cancel()
					}
					done := make(chan error, 1)
					go func() { _, err := cp.Poll(ctx); done <- err }()
					if phase == "during" {
						<-entered
						cancel()
					}
					select {
					case err := <-done:
						if !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
					case <-time.After(time.Second):
						t.Fatal("对账请求忽略取消")
					}
					if phase == "before" && calls.Load() != 0 {
						t.Fatal("已取消仍发送 HTTP")
					}
					if phase == "during" {
						select {
						case <-canceled:
						case <-time.After(time.Second):
							t.Fatal("在途请求未取消")
						}
						if calls.Load() != 1 {
							t.Fatal("取消配置请求后仍发用户请求")
						}
					}
				})
			}
		})
	}
}

func TestFailedPollCannotRestoreETagsInvalidatedDuringRequest(t *testing.T) {
	var configCalls, userCalls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/UniProxy/config" {
			call := configCalls.Add(1)
			if call >= 3 && r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(304)
				return
			}
			etag := `"v1"`
			if call == 2 {
				etag = `"v2"`
			}
			w.Header().Set("ETag", etag)
			w.Write([]byte(`{"protocol":"vless","server_port":10001}`))
		} else {
			call := userCalls.Add(1)
			if call == 2 {
				close(entered)
				<-release
				w.WriteHeader(500)
				return
			}
			if call >= 3 && r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(304)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte(`{"users":[]}`))
		}
	}))
	defer server.Close()
	cp := snapshotControlPlanes(server.URL)["node"]
	if _, err := cp.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := cp.Poll(t.Context()); done <- err }()
	<-entered
	cp.(PollStateResetter).ResetPollingState()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("部分拉取失败没有返回错误")
	}
	state, err := cp.Poll(t.Context())
	if err != nil || state.Config == nil || state.Users == nil {
		t.Fatal("失败回滚重新启用了已撤销 ETag", err)
	}
}
