package singbox

import "github.com/P0me1oo/YZ-Agent/internal/nlog"

func (s *SingBox) logger() *nlog.NodeLog {
	return nlog.Scoped(s.cfg.LogScope, s.cfg.LogNodeID, "singbox")
}
