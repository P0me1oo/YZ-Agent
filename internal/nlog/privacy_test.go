package nlog

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestPrivacyRecordsLocallyAndPublishesWithoutPlaintextCopies(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	t.Cleanup(func() { ConfigureLogPolicies(nil); Init(os.Stdout, slog.LevelInfo, false); SetGlobalScope("") })
	var console bytes.Buffer
	Init(&console, slog.LevelDebug, false)
	RegisterLogScope(key, key, filepath.Join(dir, "runtime.log.enc"), state)
	SetGlobalScope(key)
	var events []Event
	stop := Subscribe(key, func(event Event) { events = append(events, event) })
	defer stop()
	logger := Scoped(key, 7, "node:7")
	logger.Error("unknown policy")
	if len(events) != 0 {
		t.Fatal("unknown policy generated events")
	}
	ApplyLogPolicy(key, true)
	logger.Debug("private debug context")
	logger.Warn("private agent warning")
	WriteRuntimeScoped(key, "private core connection")
	Publish(key, Event{Source: "xray", Level: "error", Message: "private xray error"})
	if console.Len() != 0 || len(events) != 3 || !PolicyReady(key) {
		t.Fatal("private logs were lost or exposed")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.log.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private")) {
		t.Fatal("plaintext written to disk")
	}
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"private debug context", "private agent warning", "private core connection"} {
		if !strings.Contains(decoded.String(), marker) {
			t.Fatalf("missing encrypted record %s", marker)
		}
	}
	// 切换回普通模式不改写历史密文；再次开启沿用同一机器密钥。
	ApplyLogPolicy(key, false)
	logger.Info("normal record")
	if !strings.Contains(console.String(), "normal record") {
		t.Fatal("normal output not restored")
	}
	ApplyLogPolicy(key, true)
	console.Reset()
	Init(&console, slog.LevelError, true)
	logger.Warn("below configured level")
	logger.Error("second private error")
	if console.Len() != 0 || len(events) != 5 {
		t.Fatal("local log configuration bypassed privacy or level")
	}
}

func TestPrivateLogFailureNeverFallsBackToPlaintext(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	path := filepath.Join(dir, "runtime.log.enc")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	RegisterLogScope(key, key, path, state)
	SetGlobalScope(key)
	ApplyLogPolicy(key, true)
	var console bytes.Buffer
	Init(&console, slog.LevelInfo, false)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() {
		os.Stderr = previous
		reader.Close()
		writer.Close()
		ConfigureLogPolicies(nil)
		SetGlobalScope("")
		Init(os.Stdout, slog.LevelInfo, false)
	})
	Core().Error("private record on failed disk")
	WriteRuntime("private runtime on failed disk")
	writer.Close()
	fallback, err := io.ReadAll(reader)
	if err != nil || len(fallback) != 0 || console.Len() != 0 {
		t.Fatal("private logging failed open", err)
	}
}

func TestPrivacySeparatesMachineLogsAndProtectsSharedOutput(t *testing.T) {
	one, two := t.Name()+"/one", t.Name()+"/two"
	ConfigureLogPolicies([]string{one, two})
	t.Cleanup(func() { ConfigureLogPolicies(nil); Init(os.Stdout, slog.LevelInfo, false); SetGlobalScope("") })
	SetGlobalScope("")
	var console bytes.Buffer
	Init(&console, slog.LevelInfo, false)
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first, second := privacytest.Bind(t, firstDir), privacytest.Bind(t, secondDir)
	firstPath, secondPath := filepath.Join(firstDir, "runtime.log.enc"), filepath.Join(secondDir, "runtime.log.enc")
	RegisterLogScope(one, one, firstPath, first)
	RegisterLogScope(two, two, secondPath, second)
	ApplyLogPolicy(one, true)
	ApplyLogPolicy(two, false)
	Scoped(one, 1, "first").Error("first private record")
	Scoped(two, 2, "second").Info("second normal record")
	WriteRuntime("shared private runtime")
	if strings.Contains(console.String(), "private") || !strings.Contains(console.String(), "second normal record") {
		t.Fatal("machine policies mixed")
	}
	ApplyLogPolicy(two, true)
	Scoped(two, 2, "second").Error("second private record")
	raw, _ := os.ReadFile(firstPath)
	if err := second.ReadLog(&bytes.Buffer{}, bytes.NewReader(raw)); err == nil {
		t.Fatal("machine keys mixed")
	}
	raw, _ = os.ReadFile(secondPath)
	var decoded bytes.Buffer
	if err := second.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decoded.String(), "first private") || !strings.Contains(decoded.String(), "second private") {
		t.Fatal("machine records mixed")
	}
}

func TestFilteredSubscriptionPreservesAllLocalPrivateLevels(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	t.Cleanup(func() {
		ConfigureLogPolicies(nil)
		Init(os.Stdout, slog.LevelInfo, false)
		SetGlobalScope("")
	})
	var console bytes.Buffer
	Init(&console, slog.LevelDebug, false)
	RegisterLogScope(key, key, filepath.Join(dir, "runtime.log.enc"), state)
	ApplyLogPolicy(key, true)
	var events []Event
	stop := SubscribeFiltered(key, func(event Event) bool {
		return event.Level == "warn" || event.Level == "error"
	}, func(event Event) { events = append(events, event) })
	defer stop()
	logger := Scoped(key, 7, "test")
	logger.Debug("local debug")
	logger.Info("local info")
	logger.Warn("uploaded warning token=" + strings.Repeat("test", 8))
	logger.Error("uploaded error")
	if console.Len() != 0 || len(events) != 2 || !strings.Contains(events[0].Message, "[已隐藏]") {
		t.Fatal("level filtering changed privacy, required events or sanitization")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "runtime.log.enc"))
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"local debug", "local info", "uploaded warning", "uploaded error"} {
		if !strings.Contains(decoded.String(), marker) {
			t.Fatalf("local level was lost: %s", marker)
		}
	}
}

func TestPrivateLogWritersReleaseHandlesOnPolicyChanges(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file descriptor inspection requires Linux procfs")
	}
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	path, replacement := filepath.Join(dir, "runtime.log.enc"), filepath.Join(dir, "next", "runtime.log.enc")
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	t.Cleanup(func() { ConfigureLogPolicies(nil) })
	RegisterLogScope(key, key, path, state)
	ApplyLogPolicy(key, true)
	WriteRuntimeScoped(key, "first record")
	assertHandles := func(path string, want int) {
		t.Helper()
		files, err := filepath.Glob("/proc/self/fd/*")
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, fd := range files {
			if target, err := os.Readlink(fd); err == nil && target == path {
				got++
			}
		}
		if got != want {
			t.Fatalf("unexpected open handles: got %d, want %d", got, want)
		}
	}
	assertHandles(path, 1)
	ApplyLogPolicy(key, false)
	assertHandles(path, 0)
	ApplyLogPolicy(key, true)
	WriteRuntimeScoped(key, "reopened record")
	assertHandles(path, 1)
	RegisterLogScope(key, key, replacement, state)
	assertHandles(path, 0)
	WriteRuntimeScoped(key, "replacement record")
	assertHandles(replacement, 1)
	CloseRuntimeLogs()
	assertHandles(replacement, 0)
	WriteRuntimeScoped(key, "after close")
	ConfigureLogPolicies(nil)
	assertHandles(replacement, 0)
}
