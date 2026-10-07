package service

import (
	"context"
	"errors"
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/sourcepolicy"
)

type sourceStoreStub struct {
	snapshot sourcepolicy.Snapshot
	err      error
	calls    int
}

func (s *sourceStoreStub) Get(context.Context) (sourcepolicy.Snapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

type sourceKernelRecorder struct {
	*fakeKernel
	prepared *model.NodeSpec
}

func (k *sourceKernelRecorder) Start(n *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	k.prepared = n
	return k.fakeKernel.Start(n, users, tls)
}
func (k *sourceKernelRecorder) Reload(n *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	k.prepared = n
	return k.fakeKernel.Reload(n, users, tls)
}

func TestSourcePolicyInitialFailureRecoveryRefreshAndDisable(t *testing.T) {
	ctx := context.Background()
	k := &sourceKernelRecorder{fakeKernel: &fakeKernel{}}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	store := &sourceStoreStub{err: errors.New("测试下载失败")}
	s.sourcePolicyStore = store
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 12345, SourcePolicy: &sourcepolicy.Policy{BlockCN: true}}
	s.updateUserState([]model.UserSpec{{ID: 1}})
	if s.startKernel(ctx, s.lastConfig, s.lastUsers) || k.startCalls != 0 {
		t.Fatal("缺少数据时启动了无拦截内核")
	}
	first, err := sourcepolicy.Parse([]byte("198.51.100.0/24\n2001:db8::/32\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.applySourcePolicyRefresh(ctx, sourcePolicyResult{snapshot: first})
	if k.startCalls != 1 || !k.running || s.runtimeError != nil || !k.prepared.SourcePolicyReady {
		t.Fatal("网段库恢复后没有启动受保护的内核")
	}
	if s.lastConfig.SourcePolicyReady || len(s.lastConfig.SourceBlockCIDRs) != 0 {
		t.Fatal("运行网段污染了面板快照")
	}
	s.applySourcePolicyRefresh(ctx, sourcePolicyResult{snapshot: first})
	if k.reloadCalls != 0 {
		t.Fatal("重复网段更新触发了重载")
	}
	s.applySourcePolicyRefresh(ctx, sourcePolicyResult{snapshot: first, err: errors.New("测试更新失败")})
	if k.reloadCalls != 0 || !k.running {
		t.Fatal("下载失败没有保留原有运行状态")
	}
	second, err := sourcepolicy.Parse([]byte("203.0.113.0/24\n2001:db8::/32\n"))
	if err != nil {
		t.Fatal(err)
	}
	s.applySourcePolicyRefresh(ctx, sourcePolicyResult{snapshot: second})
	if k.reloadCalls != 1 || k.prepared.SourceBlockCIDRs[0] != "203.0.113.0/24" {
		t.Fatal("新网段未应用到运行内核")
	}
	disabled := *s.lastConfig
	disabled.SourcePolicy = nil
	if !s.applyConfigUpdate(ctx, &disabled, computeConfigHash(&disabled)) {
		t.Fatal("关闭来源拦截失败")
	}
	if k.prepared.SourcePolicy != nil || len(k.prepared.SourceBlockCIDRs) != 0 {
		t.Fatal("关闭后仍有拦截网段")
	}
	calls := k.reloadCalls
	s.applySourcePolicyRefresh(ctx, sourcePolicyResult{snapshot: first})
	if k.reloadCalls != calls {
		t.Fatal("关闭后迟到的数据库结果重新启用了限制")
	}
}

func TestSourcePolicyHashAndExceptionsRemainIndependent(t *testing.T) {
	snapshot, err := sourcepolicy.Parse([]byte("198.51.100.0/24\n2001:db8::/32\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestService(&fakeKernel{})
	s.sourcePolicySnapshot = snapshot
	n := &model.NodeSpec{Protocol: "vless", SourcePolicy: &sourcepolicy.Policy{BlockCN: true, AllowIPs: []string{"198.51.100.7"}}}
	before := computeConfigHash(n)
	prepared, err := s.prepareSourcePolicy(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	if computeConfigHash(n) != before || prepared == n {
		t.Fatal("准备网段修改了面板配置")
	}
	changed := *n
	changed.SourcePolicy = &sourcepolicy.Policy{BlockCN: true, AllowIPs: []string{"198.51.100.8"}}
	if computeConfigHash(&changed) == before {
		t.Fatal("例外变更没有进入配置哈希")
	}
	if kernel.ComputeHash(prepared, nil) == kernel.ComputeHash(n, nil) {
		t.Fatal("网段变更没有进入内核哈希")
	}
}
