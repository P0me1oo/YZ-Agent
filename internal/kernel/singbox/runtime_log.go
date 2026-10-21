package singbox

import (
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	blog "github.com/sagernet/sing-box/log"
)

type runtimeLogWriter struct {
	scope   string
	nodeID  int
	factory blog.Factory
}

func (w runtimeLogWriter) WriteMessage(level blog.Level, message string) {
	if w.factory != nil && level > w.factory.Level() {
		return
	}
	severity := blog.FormatLevel(level)
	if severity == "trace" {
		severity = "debug"
	}
	if severity == "panic" {
		severity = "fatal"
	}
	nlog.Publish(w.scope, nlog.Event{NodeID: w.nodeID, Source: "singbox", Level: severity, Message: message})
}
