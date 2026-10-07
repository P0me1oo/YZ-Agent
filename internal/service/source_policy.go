package service

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type sourcePolicyStore interface {
	Get(context.Context) (sourcepolicy.Snapshot, error)
}

type sourcePolicyResult struct {
	snapshot sourcepolicy.Snapshot
	err      error
}

// 所有状态只在 Service 主循环内修改，后台仅向结果通道发送不可变快照。
type sourcePolicyState struct {
	sourcePolicyStore       sourcePolicyStore
	sourcePolicySnapshot    sourcepolicy.Snapshot
	sourcePolicyResults     chan sourcePolicyResult
	sourcePolicyActive      bool
	sourcePolicyLastCheck   time.Time
	sourcePolicyDataFailed  bool
	sourcePolicyFailureHash string
	sourcePolicyLastError   string
}

func (s *Service) policyStore() sourcePolicyStore {
	if s.sourcePolicyStore == nil {
		dir := s.cfg.Kernel.GeoDataDir
		if dir == "" {
			dir = filepath.Join(s.cfg.Kernel.ConfigDir, "geodata")
		}
		s.sourcePolicyStore = sourcepolicy.ForDir(dir)
	}
	return s.sourcePolicyStore
}

func (s *Service) prepareSourcePolicy(ctx context.Context, nc *model.NodeSpec) (*model.NodeSpec, error) {
	if nc == nil || !nc.SourcePolicy.Enabled() {
		s.sourcePolicyDataFailed = false
		s.sourcePolicyFailureHash = ""
		return nc, nil
	}
	if !s.sourcePolicySnapshot.Ready() {
		snapshot, err := s.policyStore().Get(ctx)
		if !snapshot.Ready() {
			s.sourcePolicyDataFailed = true
			if err == nil {
				err = fmt.Errorf("大陆来源网段库尚未就绪")
			}
			return nil, err
		}
		if err != nil {
			nlog.Core().Warn("使用上一份大陆来源网段库", "error", err)
		}
		s.sourcePolicySnapshot = snapshot
	}
	blocked, err := nc.SourcePolicy.Blocked(s.sourcePolicySnapshot)
	if err != nil {
		return nil, err
	}
	prepared := *nc
	prepared.SourceBlockCIDRs = blocked
	prepared.SourcePolicyReady = true
	s.sourcePolicyDataFailed = false
	return &prepared, nil
}

func (s *Service) refreshSourcePolicyAsync(ctx context.Context) {
	if s.lastConfig == nil || !s.lastConfig.SourcePolicy.Enabled() || s.sourcePolicyActive || time.Since(s.sourcePolicyLastCheck) < time.Minute {
		return
	}
	if s.sourcePolicyResults == nil {
		s.sourcePolicyResults = make(chan sourcePolicyResult, 1)
	}
	s.sourcePolicyActive = true
	s.sourcePolicyLastCheck = time.Now()
	store, results := s.policyStore(), s.sourcePolicyResults
	go func() {
		snapshot, err := store.Get(ctx)
		select {
		case results <- sourcePolicyResult{snapshot, err}:
		case <-ctx.Done():
		}
	}()
}

func (s *Service) applySourcePolicyRefresh(ctx context.Context, result sourcePolicyResult) {
	s.sourcePolicyActive = false
	if s.lastConfig == nil || !s.lastConfig.SourcePolicy.Enabled() {
		return
	}
	if result.err != nil {
		if result.err.Error() != s.sourcePolicyLastError {
			nlog.Core().Warn("大陆来源网段库更新未完成，保留现有拦截", "error", result.err)
			s.sourcePolicyLastError = result.err.Error()
		}
	} else {
		s.sourcePolicyLastError = ""
	}
	if !result.snapshot.Ready() {
		return
	}
	if result.snapshot.Revision == s.sourcePolicySnapshot.Revision && !s.sourcePolicyDataFailed {
		return
	}
	s.sourcePolicySnapshot = result.snapshot
	// 只恢复因首次网段下载失败而停下的节点，不绕过其他配置错误的保护。
	if s.sourcePolicyDataFailed {
		if s.failedRuntimeHash == s.sourcePolicyFailureHash {
			s.failedRuntimeHash = ""
		}
		s.sourcePolicyDataFailed = false
		s.sourcePolicyFailureHash = ""
	}
	s.applyChanges(ctx, true, false)
}
