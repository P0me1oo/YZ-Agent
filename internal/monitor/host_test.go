package monitor

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestHostRatesUseMonotonicIntervalAndIndependentCounters(t *testing.T) {
	var s hostSampler
	initial := hostCounters{
		cpu: &cpuCounters{busy: 10, idle: 20},
		links: map[string]linkCounter{
			"eth0": {index: 1, rx: 100, tx: 250},
			"eth1": {index: 2, rx: 320, tx: 400},
		},
	}
	at := time.Now()
	var first, next, idle HostSample
	s.applyDeltas(at, initial, &first)
	if first.CPUPercent != nil || first.UploadBytesPerSecond != nil || first.DownloadBytesPerSecond != nil {
		t.Fatal("first counter sample has rates")
	}
	current := hostCounters{
		cpu: &cpuCounters{busy: 20, idle: 50},
		links: map[string]linkCounter{
			"eth0": {index: 1, rx: 130, tx: 340},
			"eth1": {index: 2, rx: 380, tx: 430},
		},
	}
	s.applyDeltas(at.Add(15*time.Second), current, &next)
	if *next.CPUPercent != 25 || *next.DownloadBytesPerSecond != 6 || *next.UploadBytesPerSecond != 8 {
		t.Fatalf("incorrect rates: %+v", next)
	}
	current.cpu = &cpuCounters{busy: 20, idle: 55}
	s.applyDeltas(at.Add(25*time.Second), current, &idle)
	if *idle.CPUPercent != 0 || *idle.UploadBytesPerSecond != 0 || *idle.DownloadBytesPerSecond != 0 {
		t.Fatal("valid idle host must have zero rates")
	}
}

func TestHostNetworkDiscontinuitiesLeaveGap(t *testing.T) {
	initial := map[string]linkCounter{"eth0": {index: 1, rx: 100, tx: 100}, "eth1": {index: 2, rx: 200, tx: 200}}
	cases := []struct {
		name  string
		links map[string]linkCounter
	}{
		{name: "reset masked by another interface", links: map[string]linkCounter{"eth0": {1, 1, 100}, "eth1": {2, 1000, 200}}},
		{name: "replaced with higher counters", links: map[string]linkCounter{"eth0": {3, 1000, 1500}, "eth1": {2, 200, 200}}},
		{name: "new interface", links: map[string]linkCounter{"eth0": {1, 100, 100}, "eth1": {2, 200, 200}, "eth2": {3, 300, 400}}},
		{name: "renamed interface", links: map[string]linkCounter{"wan": {1, 150, 150}, "eth1": {2, 210, 210}}},
		{name: "removed interface", links: map[string]linkCounter{"eth1": {2, 220, 210}}},
		{name: "no eligible interfaces", links: map[string]linkCounter{}},
		{name: "unreadable", links: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			at := time.Now()
			s := hostSampler{at: at, previous: hostCounters{links: initial}}
			var got HostSample
			s.applyDeltas(at.Add(hostInterval), hostCounters{links: c.links}, &got)
			if got.UploadBytesPerSecond != nil || got.DownloadBytesPerSecond != nil {
				t.Fatal("discontinuity produced a network rate")
			}
			s.applyDeltas(at.Add(2*hostInterval), hostCounters{links: c.links}, &got)
			if (got.UploadBytesPerSecond == nil) != (len(c.links) == 0) {
				t.Fatal("next complete interval must recover using the new baseline")
			}
		})
	}
}

func TestHostLongGapAndFailedReadForgetDeltas(t *testing.T) {
	at := time.Now()
	first := hostCounters{cpu: &cpuCounters{busy: 10, idle: 10}, links: map[string]linkCounter{"eth0": {index: 1}}}
	next := hostCounters{cpu: &cpuCounters{busy: 20, idle: 20}, links: map[string]linkCounter{"eth0": {index: 1, rx: 20, tx: 20}}}
	s := hostSampler{previous: first, at: at}
	steps := []struct {
		name    string
		gap     time.Duration
		current hostCounters
	}{
		{name: "sample expired", gap: hostMaxGap + time.Second, current: next},
		{name: "read failed", gap: hostInterval},
		{name: "first sample after recovery", gap: hostInterval, current: next},
		{name: "counters decreased", gap: hostInterval, current: first},
	}
	for _, step := range steps {
		at = at.Add(step.gap)
		var sample HostSample
		s.applyDeltas(at, step.current, &sample)
		if sample.CPUPercent != nil || sample.UploadBytesPerSecond != nil || sample.DownloadBytesPerSecond != nil {
			t.Fatalf("%s produced rates", step.name)
		}
	}
}

func TestHostSampleSharesOneMeasurementAcrossReaders(t *testing.T) {
	const readers = 48
	var wg sync.WaitGroup
	results := make([]HostSample, readers)
	for n := range results {
		wg.Go(func() { results[n] = CollectHostSample() })
	}
	wg.Wait()
	for _, s := range results {
		if s.SampledAt == 0 || s != results[0] {
			t.Fatal("concurrent callers must share one immutable sample")
		}
	}
}

func TestHostWireRepresentsUnavailableFields(t *testing.T) {
	data, err := json.Marshal(HostSample{SampledAt: 123})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cpu_percent", "memory_total_bytes", "memory_used_bytes", "disk_total_bytes", "disk_used_bytes", "download_bytes_per_second", "upload_bytes_per_second"} {
		if v, ok := fields[key]; !ok || v != nil {
			t.Fatalf("unavailable %s must be null, got %v", key, v)
		}
	}
}
