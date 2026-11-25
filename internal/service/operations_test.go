package service

import (
	"context"
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/autospeed"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOperationsRelayFirewallDistinguishesExecution(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		configure  func(*Service)
	}{
		{name: "applied", want: "applied"},
		{name: "missing manager", want: "pending", configure: func(s *Service) { s.firewall = nil }},
		{name: "write failed", want: "pending", configure: func(s *Service) { message := "write failed"; s.firewallNotice.Store(&message) }},
		{name: "expired", want: "pending", configure: func(s *Service) { s.lastConfig.Relay.Firewall.ValidUntil = time.Now().Unix() - 1 }},
		{name: "pending", want: "pending", configure: func(s *Service) { s.lastConfig.Relay.Firewall.Status = "pending" }},
		{name: "global disabled", want: "disabled", configure: func(s *Service) { s.cfg.Firewall.Enabled = new(false) }},
		{name: "node disabled", want: "disabled", configure: func(s *Service) { s.lastConfig.AutoFirewall = new(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &fakeKernel{running: true}
			s := newTestService(core)
			s.cfg.Firewall.Enabled = new(true)
			s.lastConfig = landingConfig()
			s.lastConfig.Relay.Firewall = &panel.RelayFirewallConfig{Version: 1, Status: "ready", ValidUntil: time.Now().Unix() + 120, Sources: []string{"8.8.8.8"}}
			s.firewall = &lifecycleFirewall{kernel: core}
			if tc.configure != nil {
				tc.configure(s)
			}
			s.updateOperations(t.Context())
			result := s.operations.snapshot.Load()
			if result.RelayFirewall != tc.want || result.Runtime != "running" {
				t.Fatalf("execution status: %+v", result)
			}
		})
	}
}

func TestOperationsFieldsDoNotRebuildRuntime(t *testing.T) {
	a := &model.NodeSpec{Protocol: "vless", ServerPort: 443}
	b := *a
	b.HealthCheck = &healthcheck.Config{Enabled: true, URL: "https://example.com/", Interval: 60}
	b.CertificateRenew = "request"
	if computeKernelConfigHash(a) != computeKernelConfigHash(&b) {
		t.Fatal("operation changed runtime hash")
	}
	if computeConfigHash(a) == computeConfigHash(&b) {
		t.Fatal("control update omitted operations")
	}
}

func TestOperationsListenerInspectionDoesNotOpenConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
			accepted <- struct{}{}
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
	})
	s := newTestService(&fakeKernel{running: true})
	s.lastUsers = []model.UserSpec{{ID: 1}}
	s.lastConfig = &model.NodeSpec{
		Protocol: "vless", ListenIP: "127.0.0.1",
		ServerPort:  listener.Addr().(*net.TCPAddr).Port,
		HealthCheck: &healthcheck.Config{Enabled: true, Interval: 60},
	}
	s.updateOperations(t.Context())
	deadline := time.After(time.Second)
	for s.operations.probe.Load() == nil {
		select {
		case <-deadline:
			t.Fatal("监听检查没有结束")
		case <-time.After(time.Millisecond):
		}
	}
	result := s.operations.probe.Load()
	if len(result.Steps) != 1 || result.Steps[0].Name != "listener" || result.Steps[0].Status != "ok" {
		t.Fatalf("监听检查未确认当前进程的监听：%+v", result)
	}
	select {
	case <-accepted:
		t.Fatal("监听检查建立了空连接，会触发入站握手错误")
	case <-time.After(25 * time.Millisecond):
	}
}

func TestOperationsProbePreferencesBelongToCurrentConfiguration(t *testing.T) {
	s := newTestService(&fakeKernel{running: true})
	s.lastUsers = []model.UserSpec{{ID: 1}}
	s.lastConfig = &model.NodeSpec{Protocol: "vless", HealthCheck: &healthcheck.Config{Enabled: true}}
	check := func() {
		t.Helper()
		s.lastConfig.HealthCheck.Request += "next"
		s.updateOperations(t.Context())
		deadline := time.After(time.Second)
		for s.operations.probing.Load() {
			select {
			case <-deadline:
				t.Fatal("运行检查未退出")
			case <-time.After(time.Millisecond):
			}
		}
	}
	check()
	previous := s.operations.probeAddresses
	check()
	if previous == nil || previous != s.operations.probeAddresses {
		t.Fatal("同一配置的重复检查未复用出站地址记录")
	}
	s.lastConfig.ServerPort++
	check()
	if previous == s.operations.probeAddresses {
		t.Fatal("新核心配置沿用了旧出站地址记录")
	}
	previous = s.operations.probeAddresses
	s.lastConfig.HealthCheck.URL = "https://example.com/check"
	check()
	if previous == s.operations.probeAddresses {
		t.Fatal("更换目标后仍使用旧地址记录")
	}
	s.lastConfig.HealthCheck.Enabled = false
	s.updateOperations(t.Context())
	if s.operations.probeAddresses != nil {
		t.Fatal("关闭检查后未清除地址记录")
	}
	s.lastConfig.HealthCheck.Enabled = true
	check()
	s.kernel.(*fakeKernel).running = false
	s.updateOperations(t.Context())
	if s.operations.probeAddresses != nil {
		t.Fatal("核心停止后未清除地址记录")
	}
}
func TestAutoSpeedPersistenceIsPrivateAndKeepsOriginalExpiry(t *testing.T) {
	cfg := &config.Config{}
	cfg.Kernel.ConfigDir = t.TempDir()
	cfg.Panel.URL = "https://panel.example.invalid"
	cfg.Panel.NodeID = 1
	s := New(cfg)
	r := &autospeed.Rule{Enabled: true, Mode: "continuous", Direction: "download", Threshold: 10, Target: 2, Duration: 3, Hold: 10}
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "example", AutoSpeedLimit: r}}
	s.limiter.UpdateUsers(s.lastUsers)
	s.speedTracker.UpdateBuckets()
	now := time.Now()
	s.userSpeed.rates = map[int][2]int64{1: {0, 2000000}}
	for i := 0; i <= 3; i++ {
		s.userSpeed.at = now.Add(time.Duration(i) * time.Second)
		s.updateAutoSpeed(now.Add(time.Duration(i) * time.Second))
	}
	path := s.autoSpeedPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	data, err := privacy.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[int]autospeed.Penalty
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved[1].Until != now.Add(13*time.Second).Unix() {
		t.Fatal(saved)
	}
	other := New(cfg)
	other.lastUsers = s.lastUsers
	other.limiter.UpdateUsers(other.lastUsers)
	other.speedTracker.UpdateBuckets()
	other.updateAutoSpeed(now.Add(8 * time.Second))
	if other.autoSpeed.active[1].Until != saved[1].Until {
		t.Fatal("restart extended penalty")
	}
	other.updateAutoSpeed(now.Add(14 * time.Second))
	if len(other.autoSpeed.active) != 0 {
		t.Fatal("expired penalty retained")
	}
}
func TestRenewalReceiptPersistsRequestIdentity(t *testing.T) {
	s := &Service{pendingStore: &pendingReportStore{path: filepath.Join(t.TempDir(), "pending.json")}}
	if err := s.saveRenewal(&renewResult{ID: "request-1", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	data, err := privacy.ReadFile(s.renewalPath())
	if err != nil {
		t.Fatal(err)
	}
	var got renewResult
	if json.Unmarshal(data, &got) != nil || got.ID != "request-1" {
		t.Fatal(string(data))
	}
}

func TestStoppedOperationsCannotReuseHealthyProbe(t *testing.T) {
	s := newTestService(&fakeKernel{})
	s.lastConfig = &model.NodeSpec{Protocol: "vless", HealthCheck: &healthcheck.Config{Enabled: true}}
	s.operations.probe.Store(&healthcheck.Result{Status: "ok", ConfigHash: computeKernelConfigHash(s.lastConfig)})
	s.updateOperations(context.Background())
	snapshot := s.operations.snapshot.Load()
	if snapshot.Runtime != "stopped" || snapshot.Health != nil || s.operations.probe.Load() != nil {
		t.Fatal("停止的核心仍展示上次健康结果", snapshot)
	}
}

func TestAutoSpeedCorruptPersistenceIsPreserved(t *testing.T) {
	cfg := &config.Config{}
	cfg.Kernel.ConfigDir = t.TempDir()
	cfg.Panel.URL = "https://panel.example.invalid"
	cfg.Panel.NodeID = 1
	s := New(cfg)
	path := s.autoSpeedPath()
	if err := os.WriteFile(path, []byte("invalid-json"), 0600); err != nil {
		t.Fatal(err)
	}
	s.updateAutoSpeed(time.Now())
	data, _ := os.ReadFile(path)
	if string(data) != "invalid-json" || s.autoSpeed.error == "" {
		t.Fatal("corrupt file overwritten or hidden")
	}
}
