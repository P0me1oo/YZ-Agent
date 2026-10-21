package service

import "github.com/P0me1oo/YZ-Agent/internal/nlog"

func (s *Service) logger() *nlog.NodeLog {
	if s.nodeLog != nil {
		return s.nodeLog
	}
	return nlog.Core()
}

// 即使共享进程无法归属原始 Xray 日志，生命周期仍能按所属节点记录。
func (s *Service) runtimeLog(status RuntimeStatus) {
	if s.nodeLog != nil {
		s.nodeLog.Info("节点运行状态变化", "status", string(status))
	}
}
