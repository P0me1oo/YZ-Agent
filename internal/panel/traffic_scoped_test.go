package panel

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
)

func TestTrafficAccountingHandshake(t *testing.T) {
	for _, tc := range []struct {
		name, selected string
		ack, wantErr   bool
	}{
		{name: "Next", selected: scopedTrafficVersion, ack: true},
		{name: "existing panel", ack: true},
		{name: "unknown selection", selected: "unsupported", ack: true, wantErr: true},
		{name: "missing receipt", selected: scopedTrafficVersion, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := newTestServer(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Versions []string `json:"traffic_accounting_versions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !slices.Contains(req.Versions, scopedTrafficVersion) {
					t.Error("scoped accounting capability missing", err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"traffic_accounting": tc.selected, "realtime": map[string]any{"traffic_ack": tc.ack, "version": 1},
				})
			})
			defer server.Close()
			_, err := client.HandshakeContext(t.Context())
			if (err != nil) != tc.wantErr || client.UsesScopedTraffic() != (tc.selected == scopedTrafficVersion && tc.ack) {
				t.Fatal("unexpected negotiation result", err)
			}
		})
	}
}

func TestScopedReportRejectsUnboundLandingLocally(t *testing.T) {
	server, client := newTestServer(func(http.ResponseWriter, *http.Request) { t.Error("unbound load sent") })
	defer server.Close()
	client.scopedAccounting.Store(true)
	if err := client.ReportScopedTraffic(t.Context(), "fixed", nil, nil, nil, map[int][2]int64{9: {1, 2}}, nil); err == nil {
		t.Fatal("logical node ID accepted as a physical landing binding")
	}
}
