package machinetraffic

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/privacy"
	"github.com/P0me1oo/YZ-Agent/internal/privacy/privacytest"
)

func TestEncryptedMeterPreservesAccumulatedTraffic(t *testing.T) {
	dir := t.TempDir()
	privacytest.Bind(t, dir)
	path := filepath.Join(dir, "meter.json")
	p := testPolicy(t)
	now := time.Unix(p.PeriodStart+3600, 0)
	reading := Reading{Boot: "private-test", Counters: map[string]Counter{"eth0": {Identity: "7", RX: 100, TX: 100}}}
	reader := func([]string) (Reading, error) { return reading, nil }
	meter := New(path, reader)
	if err := meter.Apply(p); err != nil {
		t.Fatal(err)
	}
	meter.Tick(now)
	reading.Counters["eth0"] = Counter{Identity: "7", RX: 140, TX: 160}
	before, blocked := meter.Tick(now.Add(time.Second))
	raw, _ := os.ReadFile(path)
	if !privacy.IsEncrypted(raw) {
		t.Fatal("服务器流量状态未加密")
	}
	restarted := New(path, reader)
	after, againBlocked := restarted.Snapshot()
	if before.RX != after.RX || before.TX != after.TX || before.Sequence != after.Sequence || blocked != againBlocked {
		t.Fatal("加密重启改变已累计流量")
	}
}
