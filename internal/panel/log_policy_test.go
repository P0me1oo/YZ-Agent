package panel

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestLogPolicyIsAppliedBeforeReturningPanelResponses(t *testing.T) {
	for _, endpoint := range []string{"handshake", "nodes", "control"} {
		t.Run(endpoint, func(t *testing.T) {
			var disabled atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"log_policy":{"version":1,"disabled":%t},"nodes":[],"settings":{},"command":null}`, disabled.Load())
			}))
			defer server.Close()
			key := nlog.LogPolicyKey(server.URL, 2, 0)
			nlog.ConfigureLogPolicies([]string{key})
			defer nlog.ConfigureLogPolicies(nil)
			client := NewClient(config.PanelConfig{URL: server.URL, MachineID: 2, Token: "test-only"})
			call := func() {
				var err error
				switch endpoint {
				case "handshake":
					_, err = client.Handshake()
				case "nodes":
					_, err = client.GetMachineNodes()
				case "control":
					_, err = client.ExchangeMachineControl("v2.26.0", "test-only", false, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			call()
			if nlog.LogsDisabled() {
				t.Fatal("explicit normal policy was not applied")
			}
			disabled.Store(true)
			call()
			disabled.Store(false)
			call()
			if !nlog.LogsDisabled() || client.AgentLogEnabled() {
				t.Fatal("policy replay must not restore runtime logs")
			}
		})
	}
}

func TestMissingOrUnknownLogPolicyKeepsStartupSilent(t *testing.T) {
	for _, response := range []string{`{}`, `{"log_policy":{"version":2,"disabled":false}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, response) }))
		key := nlog.LogPolicyKey(server.URL, 0, 1)
		nlog.ConfigureLogPolicies([]string{key})
		client := NewClient(config.PanelConfig{URL: server.URL, NodeID: 1})
		_, err := client.Handshake()
		if err != nil || !nlog.LogsDisabled() {
			t.Fatal("missing or unsupported policy must remain silent")
		}
		server.Close()
	}
	nlog.ConfigureLogPolicies(nil)
}
