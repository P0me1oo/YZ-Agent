package xray

import (
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	clog "github.com/xtls/xray-core/common/log"
	"testing"
)

type testLogHandler struct {
	count  int
	closed bool
}

func (h *testLogHandler) Handle(clog.Message) { h.count++ }
func (h *testLogHandler) Close() error        { h.closed = true; return nil }
func TestRuntimeLogPreservesConsoleWithoutInventingNode(t *testing.T) {
	var got []nlog.Event
	nlog.SetGlobalScope("xray-test")
	defer nlog.SetGlobalScope("")
	stop := nlog.Subscribe("xray-test", func(event nlog.Event) { got = append(got, event) })
	defer stop()
	inner := &testLogHandler{}
	handler := &runtimeLogHandler{inner: inner}
	handler.Handle(&clog.GeneralMessage{Severity: clog.Severity_Error, Content: "listener failed"})
	if inner.count != 1 || len(got) != 1 || got[0].NodeID != 0 || got[0].Source != "xray" {
		t.Fatal("shared log must keep machine attribution")
	}
	if err := handler.Close(); err != nil || !inner.closed {
		t.Fatal("core reload must close original console logger")
	}
}
