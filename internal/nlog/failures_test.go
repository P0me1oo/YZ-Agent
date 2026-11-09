package nlog

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFailureDiagnosticsAreBoundedPrivateAndRespectPolicy(t *testing.T) {
	var out bytes.Buffer
	Init(&out, slog.LevelInfo, false)
	t.Cleanup(func() { Init(os.Stdout, slog.LevelInfo, false); ConfigureLogPolicies(nil) })
	key := t.Name()
	ConfigureLogPolicies([]string{key})
	var events []Event
	stop := Subscribe(key, func(e Event) { events = append(events, e) })
	defer stop()
	logger := Scoped(key, 7, "node:7")
	var failures Failures
	err := errors.New("request failed token=test-secret https://secret.example/\nAuthorization: Basic short-credential")
	failures.Report(logger, "probe failed", err)
	if out.Len() != 0 {
		t.Fatal("unknown policy must remain silent")
	}
	ApplyLogPolicy(key, false)
	for i := 0; i < 10; i++ {
		failures.Report(logger, "probe failed", err)
	}
	if len(events) != 1 || strings.Count(out.String(), "probe failed") != 1 {
		t.Fatal("failure must be visible and bounded")
	}
	if strings.Contains(out.String(), "test-secret") || strings.Contains(out.String(), "secret.example") || strings.Contains(out.String(), "short-credential") {
		t.Fatal("diagnostic leaked credentials")
	}
	failures.Local(logger, "uploader failed", err)
	if len(events) != 1 || !strings.Contains(out.String(), "uploader failed") {
		t.Fatal("uploader diagnostics must not reenter queue")
	}
	failures.Report(logger, "probe failed", nil)
	failures.Report(logger, "probe failed", err)
	if len(events) != 2 {
		t.Fatal("new failure after recovery must be visible")
	}
	ApplyLogPolicy(key, true)
	out.Reset()
	failures.Local(logger, "private failure", err)
	if out.Len() != 0 {
		t.Fatal("privacy must suppress diagnostics")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("secret-file-path") }

func TestWriterFailureHasIndependentBoundedDiagnostic(t *testing.T) {
	ConfigureLogPolicies(nil)
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
		Init(os.Stdout, slog.LevelInfo, false)
		ConfigureLogPolicies(nil)
	})
	writerFailureAt = time.Time{}
	Init(failedWriter{}, slog.LevelInfo, false)
	Core().Error("secret record")
	WriteRuntime("secret runtime")
	ConfigureLogPolicies([]string{t.Name()})
	Core().Error("private error")
	writer.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "log output failed") != 1 || strings.Contains(string(data), "secret") || strings.Contains(string(data), "private") {
		t.Fatalf("unexpected fallback: %s", data)
	}
}
