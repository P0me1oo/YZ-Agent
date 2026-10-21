package xray

import "github.com/P0me1oo/YZ-Agent/internal/nlog"

func (x *Xray) logger() *nlog.NodeLog { return nlog.Scoped(x.cfg.LogScope, x.cfg.LogNodeID, "xray") }
