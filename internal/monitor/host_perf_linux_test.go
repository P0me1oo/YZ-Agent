package monitor

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func BenchmarkHostSample(b *testing.B) {
	var host hostSampler
	measureHostWork(b, func() { host.collect(time.Now()) })
}

func BenchmarkProcess64Nodes(b *testing.B) {
	measureHostWork(b, func() {
		// Each group starts a fresh 1-second window, as in a multi-node Agent.
		processSampler.expires = time.Time{}
		for range 64 {
			CollectWithHost(false)
		}
	})
}

func measureHostWork(b *testing.B, work func()) {
	b.Helper()
	b.ReportAllocs()
	beforeCPU, beforeRead := benchmarkProcessCounters(b)
	for b.Loop() {
		work()
	}
	afterCPU, afterRead := benchmarkProcessCounters(b)
	b.ReportMetric(float64(afterCPU-beforeCPU)/float64(b.N), "cpu-ns/op")
	b.ReportMetric(float64(afterRead-beforeRead)/float64(b.N), "read-calls/op")
}

func benchmarkProcessCounters(b *testing.B) (int64, int64) {
	b.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		b.Fatal(err)
	}
	raw, err := os.ReadFile("/proc/self/io")
	if err != nil {
		b.Fatal(err)
	}
	for line := range strings.Lines(string(raw)) {
		if text, ok := strings.CutPrefix(line, "syscr:"); ok {
			reads, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				b.Fatal(err)
			}
			return usage.Utime.Nano() + usage.Stime.Nano(), reads
		}
	}
	b.Fatal("missing process read count")
	return 0, 0
}
