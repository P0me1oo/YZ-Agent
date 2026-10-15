package service

import (
	"context"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/model"
)

type immediateKernel struct {
	*fakeKernel
	immediate int
}

func (k *immediateKernel) StopNow() { k.immediate++; k.running = false }

func TestTrafficProtectionClosesImmediatelyAndCancelledReloadCannotRestart(t *testing.T) {
	fake := &fakeKernel{running: true}
	k := &immediateKernel{fakeKernel: fake}
	s := newTestService(fake)
	s.kernel = k
	s.stopKernelFor(ErrTrafficProtection)
	if k.immediate != 1 || k.running {
		t.Fatal("quota protection did not select immediate stop")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrTrafficProtection)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.lastUsers = []model.UserSpec{{ID: 1}}
	for range 2 {
		if s.ensureRunning(ctx) || s.applyChanges(ctx, true, true) {
			t.Fatal("cancelled service restarted")
		}
	}
	if fake.startCalls != 0 || fake.reloadCalls != 0 {
		t.Fatal("late config bypassed quota gate")
	}
}
