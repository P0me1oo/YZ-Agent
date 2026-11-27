package panel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/config"
)

type accountingTransport struct{ requests, bytes int }

func (t *accountingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	n, err := io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	t.requests++
	t.bytes += int(n)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"receipt":{"accepted":true,"report_id":"benchmark"}}`)), Header: make(http.Header)}, err
}

// A reliable traffic batch and the current device state have different
// lifetimes. Measure the encoding cost with 1 billed user and 1,024 online.
func BenchmarkMonitorRealtimeAccountingReport(b *testing.B) {
	c := NewClient(config.PanelConfig{URL: "http://panel.test", MachineID: 1, NodeID: 1})
	transport := &accountingTransport{}
	c.httpClient.Transport = transport
	c.realtime.enabled.Store(true)
	active := make(map[int][]string, 1024)
	online := make(map[int]int, 1024)
	traffic := map[int][2]int64{1: {12, 34}}
	relay := map[int][2]int64{3: {5, 6}}
	relayUsers := map[int]map[int][2]int64{1: relay}
	for id := 1; id <= 1024; id++ {
		active[id] = []string{"8.8.8.8"}
		online[id] = 1
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		err := c.ReportContext(context.Background(), "benchmark", traffic, relay, relayUsers, nil, active, online, 32, [2]uint64{}, [2]uint64{}, [2]uint64{}, nil, nil)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(transport.bytes)/float64(b.N), "wire-B/op")
	b.ReportMetric(float64(transport.requests)/float64(b.N), "requests/op")
}
