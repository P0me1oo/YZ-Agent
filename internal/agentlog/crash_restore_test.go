package agentlog

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
)

func TestXrayCrashBatchSurvivesRestore(t *testing.T) {
	c := &collector{path: filepath.Join(t.TempDir(), "pending.json")}
	c.add(nlog.Event{NodeID: 7, Source: "xray", Level: "error", Event: "xray.crash", Message: "listener failed", OccurredAt: time.Now().Unix()})
	batch := c.next()
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	restored := &collector{path: c.path}
	if err := restored.restore(); err != nil {
		t.Fatal(err)
	}
	got := restored.next()
	if got.ID != batch.ID || len(got.Records) != 1 || got.Records[0] != batch.Records[0] {
		t.Fatal("恢复后必须保留故障事件与批次编号")
	}
}
