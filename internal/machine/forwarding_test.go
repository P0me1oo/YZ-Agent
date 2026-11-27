//go:build linux

package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/portforward"
)

func forwardingFixture(t *testing.T) (*portforward.Manager, portforward.Policy, string, string) {
	t.Helper()
	dir, bin := t.TempDir(), t.TempDir()
	marker := filepath.Join(bin, "started")
	script := fmt.Sprintf("#!/bin/sh\n: > %s\nexec /bin/sleep 60\n", marker)
	if err := os.WriteFile(filepath.Join(bin, "realm"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	m, err := portforward.New(dir, "machine-test")
	if err != nil {
		t.Fatal(err)
	}
	p := portforward.Policy{Version: 1, Rules: []portforward.Rule{{ID: 1, Mode: "realm", ListenPort: port, Target: portforward.Endpoint{Address: "1.1.1.1", Port: 443}, Network: "tcp"}}}
	if err := m.Save(p); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "forwarding-*", "policy.json"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, p, marker, files[0]
}

func TestForwardingRevocationAppliesWhenCacheCannotBeSaved(t *testing.T) {
	m, p, _, policyPath := forwardingFixture(t)
	var count atomic.Int32
	result := make(chan []portforward.Status, 1)
	failures := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Status []portforward.Status `json:"forwarding_status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failures <- err
			w.WriteHeader(500)
			return
		}
		n := count.Add(1)
		if n == 1 {
			_ = json.NewEncoder(w).Encode(p)
			return
		}
		if n == 2 {
			if len(body.Status) != 1 || body.Status[0].State != "running" {
				failures <- fmt.Errorf("initial forwarding did not start: %v", body.Status)
			}
			if err := os.Remove(policyPath); err != nil {
				failures <- err
				w.WriteHeader(500)
				return
			}
			if err := os.Mkdir(policyPath, 0700); err != nil {
				failures <- err
				w.WriteHeader(500)
				return
			}
		} else {
			select {
			case result <- body.Status:
			default:
			}
		}
		_ = json.NewEncoder(w).Encode(portforward.Policy{Version: 1, Rules: []portforward.Rule{}})
	}))
	defer server.Close()
	o := &Orchestrator{client: panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1, Token: "test"})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); o.forwardingLoop(ctx, m, nil) }()
	defer func() { cancel(); <-done }()
	select {
	case err := <-failures:
		t.Fatal(err)
	case statuses := <-result:
		if len(statuses) != 1 || statuses[0].ID != 0 || statuses[0].State != "stopped" || statuses[0].Warning == "" {
			t.Fatal("revocation was not applied", statuses)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("missing revocation report")
	}
}

func TestForwardingStartupDoesNotAuthorizeCachedRules(t *testing.T) {
	m, _, marker, _ := forwardingFixture(t)
	requests := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		select {
		case requests <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	o := &Orchestrator{client: panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1, Token: "test"})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); o.forwardingLoop(ctx, m, nil) }()
	defer func() { cancel(); <-done }()
	for range 2 {
		select {
		case <-requests:
		case <-time.After(8 * time.Second):
			t.Fatal("missing retry")
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cached rule started without online authorization", err)
	}
}

func TestTrafficProtectionRevokesForwardingDuringExchange(t *testing.T) {
	m, p, _, path := forwardingFixture(t)
	arrived := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 2 {
			close(arrived)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(p)
	}))
	defer server.Close()
	o := &Orchestrator{cfg: &config.Config{}, client: panel.NewClient(config.PanelConfig{URL: server.URL, MachineID: 1, Token: "test"}), forwardingWake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); o.forwardingLoop(ctx, m, nil) }()
	defer func() { cancel(); <-done }()
	select {
	case <-arrived:
	case <-time.After(8 * time.Second):
		t.Fatal("no in-flight forwarding exchange")
	}
	o.setTrafficBlocked(true)
	wait := func(want int) {
		t.Helper()
		end := time.Now().Add(7 * time.Second)
		for time.Now().Before(end) {
			var got portforward.Policy
			raw, e := os.ReadFile(path)
			if e == nil && json.Unmarshal(raw, &got) == nil && len(got.Rules) == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("expected %d grants", want)
	}
	wait(0)
	close(release)
	o.setTrafficBlocked(false)
	wait(1)
}
