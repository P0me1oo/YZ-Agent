package service

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

func landingConfig() *model.NodeSpec {
	return &model.NodeSpec{
		Protocol:   "shadowsocks",
		ServerPort: 28388,
		Relay: &model.RelayConfig{
			Mode:       panel.RelayModeLanding,
			Protocol:   "shadowsocks",
			ListenPort: 28388,
			Cipher:     "2022-blake3-aes-128-gcm",
			Password:   "MTIzNDU2Nzg5MGFiY2RlZg==",
		},
	}
}

func entryConfig() *model.NodeSpec {
	return &model.NodeSpec{
		Protocol:   "vless",
		ServerPort: 24443,
		Relay: &model.RelayConfig{
			Mode:    panel.RelayModeEntry,
			RouteID: 11,
			Children: []model.RelayChild{{
				NodeID: 7, Tag: "relay-7", RouteID: 12,
				Protocol: "shadowsocks", Address: "203.0.113.7", Port: 28388,
				Cipher: "2022-blake3-aes-128-gcm", Password: "MTIzNDU2Nzg5MGFiY2RlZg==",
			}},
		},
	}
}

// The landing node has no panel users by design, so the kernel must still come up.
func TestEnsureRunning_LandingStartsWithoutUsers(t *testing.T) {
	k := &fakeKernel{protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	s.lastConfig = landingConfig()

	if !s.ensureRunning(context.Background()) {
		t.Fatal("landing kernel did not start with an empty user set")
	}
	if k.startCalls != 1 {
		t.Fatalf("startCalls = %d, want 1", k.startCalls)
	}
}

func TestEnsureRunning_PlainNodeStillNeedsUsers(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 443}

	if s.ensureRunning(context.Background()) {
		t.Fatal("plain node started without users")
	}
	if k.startCalls != 0 {
		t.Fatalf("startCalls = %d, want 0", k.startCalls)
	}
}

// A config re-sync must not shut a landing node down just because it reports no users.
func TestApplyChanges_LandingSurvivesEmptyUserSet(t *testing.T) {
	k := &fakeKernel{running: true, protocols: []string{"shadowsocks"}}
	s := newTestService(k)
	s.lastConfig = landingConfig()
	s.updateUserState(nil)

	s.applyChanges(context.Background(), true, false)

	if !k.running {
		t.Fatal("landing kernel was stopped on an empty user set")
	}
}

func TestApplyChanges_PlainNodeStopsOnEmptyUserSet(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 443}
	s.updateUserState(nil)

	s.applyChanges(context.Background(), true, false)

	if k.running {
		t.Fatal("plain node kept running with no users")
	}
}

// Relay traffic collection is entry-only and must tolerate kernels without the
// optional capability.
func TestTrackRelayTraffic_SkipsNonEntryAndPlainKernel(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.tracker = tracker.New()

	s.lastConfig = landingConfig()
	s.trackRelayTraffic(context.Background())

	s.lastConfig = entryConfig()
	s.trackRelayTraffic(context.Background()) // fakeKernel is not a RelayTrafficReader

	if got := s.tracker.FlushRelayTraffic(); got != nil {
		t.Fatalf("relay traffic = %v, want nil", got)
	}
}
