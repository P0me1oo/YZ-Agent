package service

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
)

// 只保存配置指纹，避免重复轮询或重复事件触发同一份失败配置的启动。
func runtimeAttemptHash(nc *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) string {
	return fmt.Sprintf("%s:%s:%x:%x", computeConfigHash(nc), computeUserHash(users),
		sha256.Sum256(tls.CertPEM), sha256.Sum256(tls.KeyPEM))
}

func (s *Service) runtimeAttemptBlocked(nc *model.NodeSpec, users []model.UserSpec) bool {
	return s.failedRuntimeHash != "" && s.failedRuntimeHash == runtimeAttemptHash(nc, users, s.tlsCert())
}

func (s *Service) tlsCert() kernel.TLSCert {
	if s.cert == nil {
		return kernel.TLSCert{}
	}
	return s.cert.TLSCert()
}

// 失败只终止当前节点的运行，保留控制通道接收修正配置，不再启动旧配置。
func (s *Service) failRuntime(operation string, nc *model.NodeSpec, users []model.UserSpec, err error) {
	s.failedRuntimeHash = runtimeAttemptHash(nc, users, s.tlsCert())
	s.runtimeError = fmt.Errorf("%s: %w", operation, err)
	logger := nlog.Core()
	if nc != nil {
		logger = nlog.ForNode(nc.Protocol, nc.ServerPort)
	}
	logger.Error(operation+"，内核已停止", "kernel", s.kernel.Name(), "error", err)
	s.stopKernel()
	timesync.Default().SetUsage(s.timeConsumer, false)
	s.appliedState.Config = nil
	s.appliedState.Users = nil
	s.notifyStatus(RuntimeFailed)
}

// prepareRuntime 统一处理首次启动、重载和用户变化后的恢复。
// 失败状态只在成功应用后清除，避免用户更新绕过仍有错误的配置或证书。
func (s *Service) prepareRuntime(ctx context.Context, nc *model.NodeSpec, users []model.UserSpec) bool {
	if s.runtimeAttemptBlocked(nc, users) {
		return false
	}
	if nc != nil && nc.CertConfig == nil {
		if _, err := s.cert.Reconfigure(ctx, s.cfg.Cert); err != nil {
			s.failRuntime("应用本地证书失败", nc, users, err)
			return false
		}
	}
	if err := validateNodeRuntime(s.cfg, s.kernel.Protocols(), nc, s.tlsCert()); err != nil {
		s.failRuntime("配置校验失败", nc, users, err)
		return false
	}
	if _, err := s.applyRemoteOverrides(ctx, nc); err != nil {
		s.failRuntime("应用远程配置失败", nc, users, err)
		return false
	}
	s.setTimeUsage(nc)
	return true
}

func (s *Service) runtimeApplied(nc *model.NodeSpec, users []model.UserSpec) {
	s.failedRuntimeHash = ""
	s.runtimeError = nil
	s.appliedState.Config = nc
	s.appliedState.Users = users
	s.notifyStatus(RuntimeRunning)
}
