package singbox

import (
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	blog "github.com/sagernet/sing-box/log"
	"testing"
)

func TestRuntimeLogNodeAttribution(t *testing.T) {
	var got []nlog.Event
	stop := nlog.Subscribe("singbox-test", func(event nlog.Event) { got = append(got, event) })
	defer stop()
	writer := runtimeLogWriter{scope: "singbox-test", nodeID: 9}
	writer.WriteMessage(blog.LevelWarn, "listener failed")
	if len(got) != 1 || got[0].NodeID != 9 || got[0].Source != "singbox" || got[0].Level != "warn" {
		t.Fatal("runtime attribution lost")
	}
}
