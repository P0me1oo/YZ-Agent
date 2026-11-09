package machine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestControlExchangeFailureIsLogged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	cfg := &config.Config{InstanceID: t.Name(), Panel: config.PanelConfig{URL: server.URL}, Machine: &config.MachineConfig{MachineID: 7, Token: "test-token"}}
	orchestrator := New(cfg)
	nlog.ConfigureLogPolicies(nil)
	nlog.Init(io.Discard, slog.LevelInfo, false)
	t.Cleanup(func() { nlog.Init(os.Stdout, slog.LevelInfo, false) })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events := make(chan nlog.Event, 1)
	stop := nlog.Subscribe(cfg.LogScope(), func(e nlog.Event) { events <- e; cancel() })
	defer stop()
	orchestrator.controlLoop(ctx)
	select {
	case e := <-events:
		if e.Level != "warn" || !strings.Contains(e.Message, "machine control exchange failed") || !strings.Contains(e.Message, "503") {
			t.Fatal(e)
		}
	default:
		t.Fatal("control error was silently ignored")
	}
}
