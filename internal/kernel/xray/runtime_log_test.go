package xray

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
	clog "github.com/xtls/xray-core/common/log"
)

func TestRuntimeLogPrivacyEncryptsAndReportsSharedXrayLogs(t *testing.T) {
	dir := t.TempDir()
	state := privacytest.Bind(t, dir)
	scope := t.Name()
	nlog.ConfigureLogPolicies([]string{scope})
	nlog.RegisterLogScope(scope, scope, filepath.Join(dir, "runtime.log.enc"), state)
	nlog.SetGlobalScope(scope)
	nlog.ApplyLogPolicy(scope, true)
	t.Cleanup(func() {
		nlog.ConfigureLogPolicies(nil)
		nlog.SetGlobalScope("")
		nlog.Init(os.Stdout, slog.LevelInfo, false)
	})
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelInfo, false)
	events := []nlog.Event{}
	stop := nlog.Subscribe(scope, func(event nlog.Event) { events = append(events, event) })
	defer stop()
	handler := &runtimeLogHandler{}
	handler.Handle(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "private xray listener failure"})
	if console.Len() != 0 || len(events) != 1 || events[0].NodeID != 0 {
		t.Fatal("private Xray logs were exposed, dropped or assigned an invented node")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.log.enc"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded bytes.Buffer
	if err := state.ReadLog(&decoded, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private xray")) || !strings.Contains(decoded.String(), "private xray listener failure") {
		t.Fatal("Xray runtime log was not encrypted")
	}
}

func TestRuntimeLogPreservesConsoleWithoutInventingNode(t *testing.T) {
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelInfo, false)
	defer nlog.Init(os.Stdout, slog.LevelInfo, false)
	var got []nlog.Event
	nlog.SetGlobalScope("xray-test")
	defer nlog.SetGlobalScope("")
	stop := nlog.Subscribe("xray-test", func(event nlog.Event) { got = append(got, event) })
	defer stop()
	handler := &runtimeLogHandler{}
	handler.Handle(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "listener failed"})
	if console.Len() == 0 || len(got) != 1 || got[0].NodeID != 0 || got[0].Source != "xray" {
		t.Fatal("shared log must keep machine attribution")
	}
	if err := handler.Close(); err != nil {
		t.Fatal("core reload must close controlled logger")
	}
}

func TestRuntimeLogPrivacySkipsConsoleAndPanel(t *testing.T) {
	var console bytes.Buffer
	nlog.Init(&console, slog.LevelInfo, false)
	defer nlog.Init(os.Stdout, slog.LevelInfo, false)
	nlog.ConfigureLogPolicies([]string{t.Name()})
	nlog.ApplyLogPolicy(t.Name(), true)
	defer nlog.ConfigureLogPolicies(nil)
	var count int
	nlog.SetGlobalScope("xray-privacy-test")
	defer nlog.SetGlobalScope("")
	stop := nlog.Subscribe("xray-privacy-test", func(nlog.Event) { count++ })
	defer stop()
	handler := &runtimeLogHandler{}
	handler.Handle(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "937 users online"})
	if console.Len() != 0 || count != 0 {
		t.Fatal("raw Xray logs bypassed privacy")
	}
	nlog.ApplyLogPolicy(t.Name(), false)
	handler.Handle(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "restored"})
	if console.Len() == 0 || count != 1 {
		t.Fatal("Xray console and events did not resume")
	}
	if err := handler.Close(); err != nil {
		t.Fatal("privacy must not change logger cleanup")
	}
}
