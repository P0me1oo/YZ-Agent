package machine

import "github.com/P0me1oo/YZ-Agent/internal/nlog"

func (o *Orchestrator) logger() *nlog.NodeLog {
	if o.cfg == nil {
		return nlog.Core()
	}
	return nlog.Scoped(o.cfg.LogScope(), 0, "agent")
}
