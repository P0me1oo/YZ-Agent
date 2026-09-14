package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSignalDuringReloadWaitTriggersShutdown(t *testing.T) {
	done := make(chan struct{})
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	sig, shuttingDown := waitForReloadStop(done, signals)
	if !shuttingDown || sig != syscall.SIGTERM {
		t.Fatalf("waitForReloadStop() = (%v, %v), want SIGTERM and true", sig, shuttingDown)
	}

	close(done)
	if result := waitForShutdown(done, signals, time.Second); result != shutdownComplete {
		t.Fatalf("waitForShutdown() = %v, want shutdownComplete", result)
	}
}

func TestWaitForShutdownDetectsSecondSignalAndTimeout(t *testing.T) {
	t.Run("second signal", func(t *testing.T) {
		done := make(chan struct{})
		signals := make(chan os.Signal, 1)
		signals <- syscall.SIGTERM
		if result := waitForShutdown(done, signals, time.Second); result != shutdownSecondSignal {
			t.Fatalf("waitForShutdown() = %v, want shutdownSecondSignal", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		done := make(chan struct{})
		signals := make(chan os.Signal)
		if result := waitForShutdown(done, signals, time.Millisecond); result != shutdownTimedOut {
			t.Fatalf("waitForShutdown() = %v, want shutdownTimedOut", result)
		}
	})
}
