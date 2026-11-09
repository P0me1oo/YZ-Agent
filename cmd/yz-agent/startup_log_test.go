package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestStartupFailureConfirmsPolicyBeforePrinting(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		visible        bool
	}{
		{"allowed", `{"log_policy":{"version":1,"disabled":false}}`, true},
		{"disabled", `{"log_policy":{"version":1,"disabled":true}}`, false},
		{"unknown", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(tc.response)) }))
			defer server.Close()
			var out bytes.Buffer
			nlog.Init(&out, slog.LevelInfo, false)
			defer nlog.Init(os.Stdout, slog.LevelInfo, false)
			defer nlog.ConfigureLogPolicies(nil)
			root := &config.RootConfig{Config: config.Config{Panel: config.PanelConfig{URL: server.URL, NodeID: 7, Token: "test-token"}}}
			startupFailure(root, "startup failed", errors.New("disk token=test-secret https://secret.example/"))
			if strings.Contains(out.String(), "startup failed") != tc.visible {
				t.Fatalf("wrong policy output: %q", out.String())
			}
			if strings.Contains(out.String(), "test-secret") || strings.Contains(out.String(), "secret.example") {
				t.Fatal("startup log leaked credentials")
			}
		})
	}
}
