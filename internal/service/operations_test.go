package service

import (
	"context"
	"encoding/json"
	"github.com/P0me1oo/YZ-Agent/internal/autospeed"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
