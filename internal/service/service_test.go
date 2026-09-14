package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/cert"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
	"golang.org/x/time/rate"
)

type fakeKernel struct {
	running   bool
	protocols []string

	startErr  error
	reloadErr error
	updateErr error
	addErr    error
	removeErr error

	startCalls  int
	reloadCalls int
	updateCalls int
	addCalls    int
	removeCalls int
	startUsers  []model.UserSpec

	onUpdateUsers func([]model.UserSpec)
	onAddUsers    func([]model.UserSpec)
	onRemoveUsers func([]model.UserSpec)

	speedLimitFunc  func(string) *rate.Limiter
	deviceLimitFunc func(string) (int, bool)
}

type certRenewalPollSource struct {
	controlplane.ControlPlane
	pollErr error
}

func (p *certRenewalPollSource) SupportsPolling() bool { return true }
func (p *certRenewalPollSource) Poll(context.Context) (controlplane.Snapshot, error) {
	return controlplane.Snapshot{}, p.pollErr
}

func (f *fakeKernel) Name() string { return "fake" }
func (f *fakeKernel) Protocols() []string {
	if f.protocols != nil {
		return f.protocols
	}
	return []string{"vless"}
}
func (f *fakeKernel) Capabilities() kernel.Capabilities { return kernel.Capabilities{} }
func (f *fakeKernel) Start(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.startUsers = append([]model.UserSpec(nil), users...)
	f.running = true
	return nil
}
func (f *fakeKernel) Stop()           { f.running = false }
func (f *fakeKernel) IsRunning() bool { return f.running }
func (f *fakeKernel) Reload(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	f.reloadCalls++
	return f.reloadErr
}

func (f *fakeKernel) AddUsers(users []model.UserSpec) (int, error) {
	f.addCalls++
	if f.onAddUsers != nil {
		f.onAddUsers(users)
	}
	if f.addErr != nil {
		return 0, f.addErr
	}
	return len(users), nil
}
func (f *fakeKernel) RemoveUsers(users []model.UserSpec) (int, error) {
	f.removeCalls++
	if f.onRemoveUsers != nil {
		f.onRemoveUsers(users)
	}
	if f.removeErr != nil {
		return 0, f.removeErr
	}
	return len(users), nil
}
func (f *fakeKernel) UpdateUsers(users []model.UserSpec) (int, int, error) {
	f.updateCalls++
	if f.onUpdateUsers != nil {
		f.onUpdateUsers(users)
	}
	if f.updateErr != nil {
		return 0, 0, f.updateErr
	}
	return len(users), 0, nil
}
func (f *fakeKernel) GetUserTraffic(ctx context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	_ = ctx
	return nil, nil, 0, nil
}
func (f *fakeKernel) CloseConnection(ctx context.Context, connID string) error {
	_, _ = ctx, connID
	return nil
}
func (f *fakeKernel) CloseUserConnections(ctx context.Context, uuid string) error {
	_, _ = ctx, uuid
	return nil
}
func (f *fakeKernel) SetSpeedLimitFunc(fn func(uuid string) *rate.Limiter) { f.speedLimitFunc = fn }
func (f *fakeKernel) SetDeviceLimitFunc(fn func(uuid string) (int, bool))  { f.deviceLimitFunc = fn }
func (f *fakeKernel) UpdateGlobalDevices(users map[int][]string)           { _ = users }
func (f *fakeKernel) ClearGlobalDevices()                                  {}

func newTestService(k *fakeKernel) *Service {
	sharedLimiter := limiter.New()
	s := &Service{
		cfg:          &config.Config{Kernel: config.KernelConfig{Type: "xray"}},
		kernel:       k,
		tracker:      tracker.New(),
		limiter:      sharedLimiter,
		speedTracker: limiter.NewSpeedTracker(sharedLimiter),
		cert:         cert.NewManager(config.CertConfig{}),
	}
	k.SetSpeedLimitFunc(s.speedTracker.GetLimiter)
	k.SetDeviceLimitFunc(s.limiter.GetDeviceLimitByUUID)
	return s
}

func TestPullConsumesCertRenewalOnlyAfterSuccessfulPoll(t *testing.T) {
	for _, test := range []struct {
		name      string
		pollErr   error
		wantCalls int
		wantEvent bool
	}{
		{name: "poll failure", pollErr: errors.New("panel unavailable")},
		{name: "poll success", wantCalls: 1, wantEvent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newTestService(&fakeKernel{})
			s.source = &certRenewalPollSource{pollErr: test.pollErr}
			s.pullResults = make(chan pullResult, 1)
			calls := 0
			s.certRenewed = func() bool {
				calls++
				return true
			}

			s.pullViaAPIAsync(context.Background())
			deadline := time.Now().Add(time.Second)
			for s.pullActive.Load() && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if s.pullActive.Load() {
				t.Fatal("REST 轮询未结束")
			}
			if calls != test.wantCalls {
				t.Fatalf("证书续期标记读取次数 = %d，预期 %d", calls, test.wantCalls)
			}
			select {
			case result := <-s.pullResults:
				if !test.wantEvent || !result.certChanged {
					t.Fatalf("轮询结果中的续期状态不正确: %#v", result)
				}
			default:
				if test.wantEvent {
					t.Fatal("成功轮询未返回证书续期结果")
				}
			}
		})
	}
}

func TestApplyConfigUpdateStopsWithoutRestoringOldKernel(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		reloadErr: errors.New("reload failed"),
	}
	s := newTestService(k)
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "uuid-1"}}
	oldConfig := &model.NodeSpec{Protocol: "vless", ServerPort: 1001}
	newConfig := &model.NodeSpec{Protocol: "vless", ServerPort: 1002}
	s.lastConfig = oldConfig
	s.lastConfigHash = computeConfigHash(oldConfig)

	if s.applyConfigUpdate(context.Background(), newConfig, computeConfigHash(newConfig)) {
		t.Fatal("expected config apply to fail")
	}
	if s.lastConfig != newConfig {
		t.Fatalf("lastConfig = %#v, want failed config", s.lastConfig)
	}
	if s.lastConfigHash != computeConfigHash(newConfig) {
		t.Fatalf("lastConfigHash = %q, want failed config hash", s.lastConfigHash)
	}
	if k.running {
		t.Fatal("kernel remained running after failed reload")
	}
	if s.runtimeError == nil {
		t.Fatal("runtime failure was not recorded")
	}

	k.reloadErr = nil
	if s.applyConfigUpdate(context.Background(), newConfig, computeConfigHash(newConfig)) {
		t.Fatal("same failed config must remain blocked")
	}
	if k.startCalls != 0 {
		t.Fatalf("same failed config triggered Start %d times", k.startCalls)
	}

	changed := &model.NodeSpec{Protocol: "vless", ServerPort: 1003}
	if !s.applyConfigUpdate(context.Background(), changed, computeConfigHash(changed)) {
		t.Fatal("changed config should be allowed to start")
	}
	if k.startCalls != 1 || !k.running {
		t.Fatalf("changed config did not start kernel: calls=%d running=%v", k.startCalls, k.running)
	}
}

func TestApplyConfigUpdateStopsWhenRemoteCertificateConfigFails(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "uuid-1"}}
	configWithBadCert := &model.NodeSpec{
		Protocol:   "vless",
		ServerPort: 1004,
		CertConfig: &config.CertConfig{CertMode: "unsupported-test-mode"},
	}

	if s.applyConfigUpdate(context.Background(), configWithBadCert, computeConfigHash(configWithBadCert)) {
		t.Fatal("invalid remote certificate configuration must fail")
	}
	if k.running {
		t.Fatal("kernel remained running after remote certificate failure")
	}
	if k.reloadCalls != 0 || k.startCalls != 0 {
		t.Fatalf("kernel was applied after certificate failure: reload=%d start=%d", k.reloadCalls, k.startCalls)
	}
	if s.runtimeError == nil || !strings.Contains(s.runtimeError.Error(), "runtime certificate configuration") {
		t.Fatalf("runtime certificate failure was not recorded: %v", s.runtimeError)
	}
}

func TestWSConfigValidationFailureStopsKernelAndKeepsPendingConfig(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 1001}
	s.lastConfigHash = computeConfigHash(s.lastConfig)
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "uuid-1"}})

	bad := &model.NodeSpec{Protocol: "unsupported", ServerPort: 1002}
	s.handleWSEvent(context.Background(), controlplane.Event{Type: controlplane.EventSyncConfig, Config: bad})

	if s.lastConfig != bad || s.lastConfigHash != computeConfigHash(bad) {
		t.Fatalf("failed config was not retained: config=%#v hash=%q", s.lastConfig, s.lastConfigHash)
	}
	if k.running || s.runtimeError == nil {
		t.Fatalf("validation failure did not stop runtime: running=%v error=%v", k.running, s.runtimeError)
	}
}

func TestTrackAndEnforceDoesNotStartUnexpectedlyStoppedKernel(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 1001}
	s.lastUsers = []model.UserSpec{{ID: 1, UUID: "uuid-1"}}

	s.trackAndEnforce(context.Background())

	if k.running || k.startCalls != 0 {
		t.Fatalf("traffic sampling restarted kernel: running=%v startCalls=%d", k.running, k.startCalls)
	}
}

func TestApplyUserUpdatePreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onUpdateUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to UpdateUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected new user's limiter to be visible before kernel UpdateUsers")
		}
	}

	s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers))

	if got := k.updateCalls; got != 1 {
		t.Fatalf("UpdateUsers call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want new users", s.lastUsers)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for new user after successful update")
	}
}

func TestApplyUserUpdateStartsStoppedKernelForFirstUsers(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}

	users := []model.UserSpec{{ID: 1, UUID: "uuid-new", SpeedLimit: 8}}
	s.applyUserUpdate(context.Background(), users, computeUserHash(users))

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if got := k.updateCalls; got != 0 {
		t.Fatalf("UpdateUsers call count = %d, want 0 after starting with complete user set", got)
	}
	if len(k.startUsers) != 1 || k.startUsers[0].UUID != "uuid-new" {
		t.Fatalf("started users = %#v, want new user", k.startUsers)
	}
	if !k.running {
		t.Fatal("kernel is not running after first user update")
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want new users", s.lastUsers)
	}
}

func TestApplyUserUpdateKeepsFailedSnapshotWithoutRetry(t *testing.T) {
	k := &fakeKernel{startErr: errors.New("start failed")}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	if s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers)) {
		t.Fatal("expected initial start to fail")
	}

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want failed users to remain pending", s.lastUsers)
	}
	if s.lastUserHash != computeUserHash(newUsers) {
		t.Fatalf("lastUserHash = %q, want failed user hash", s.lastUserHash)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected failed user's limiter to remain pending")
	}
	if s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers)) {
		t.Fatal("same failed user snapshot must remain blocked")
	}
	if k.startCalls != 1 {
		t.Fatalf("same failed user snapshot retried Start: calls=%d", k.startCalls)
	}

	k.startErr = nil
	changedUsers := []model.UserSpec{{ID: 3, UUID: "uuid-corrected", SpeedLimit: 8}}
	if !s.applyUserUpdate(context.Background(), changedUsers, computeUserHash(changedUsers)) {
		t.Fatal("changed user snapshot should be allowed to start")
	}
	if k.startCalls != 2 || !k.running {
		t.Fatalf("changed user snapshot did not start kernel: calls=%d running=%v", k.startCalls, k.running)
	}
}

func TestApplyUserDeltaStartsStoppedKernelForFirstUser(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}

	delta := []model.UserSpec{{ID: 1, UUID: "uuid-new", SpeedLimit: 8}}
	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if got := k.addCalls; got != 0 {
		t.Fatalf("AddUsers call count = %d, want 0 after starting with complete user set", got)
	}
	if len(k.startUsers) != 1 || k.startUsers[0].UUID != "uuid-new" {
		t.Fatalf("started users = %#v, want new user", k.startUsers)
	}
	if !k.running {
		t.Fatal("kernel is not running after first user delta")
	}
}

func TestApplyUserDeltaKeepsFailedSnapshotWithoutRetry(t *testing.T) {
	k := &fakeKernel{startErr: errors.New("start failed")}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	if s.applyUserDelta(
		context.Background(),
		"add",
		[]model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}},
	) {
		t.Fatal("expected initial start to fail")
	}

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 2 {
		t.Fatalf("lastUsers = %#v, want merged failed users to remain pending", s.lastUsers)
	}
	if s.lastUserHash != computeUserHash(s.lastUsers) {
		t.Fatalf("lastUserHash = %q, want failed user hash", s.lastUserHash)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected failed delta user's limiter to remain pending")
	}
	if s.applyUserDelta(context.Background(), "add", []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}) {
		t.Fatal("same failed delta must remain blocked")
	}
	if k.startCalls != 1 {
		t.Fatalf("same failed delta retried Start: calls=%d", k.startCalls)
	}
}

func TestApplyUserUpdateStopsWhenKernelUpdateFails(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		updateErr: errors.New("update failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	if s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers)) {
		t.Fatal("expected user update to fail")
	}

	if got := k.startCalls; got != 0 {
		t.Fatalf("failed user update triggered restart: calls=%d", got)
	}
	if k.running {
		t.Fatal("kernel remained running after failed user update")
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want failed users to remain pending", s.lastUsers)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected failed user's limiter to remain pending")
	}
	if s.runtimeError == nil {
		t.Fatal("runtime failure was not recorded")
	}
}

func TestApplyUserDeltaAddPreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	delta := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onAddUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to AddUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected delta user's limiter to be visible before kernel AddUsers")
		}
	}

	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.addCalls; got != 1 {
		t.Fatalf("AddUsers call count = %d, want 1", got)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for delta-added user after successful update")
	}
}

func TestApplyUserDeltaUUIDChangeUsesFullUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 15, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	delta := []model.UserSpec{{ID: 15, UUID: "uuid-new", SpeedLimit: 4}}
	k.onUpdateUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].ID != 15 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to UpdateUsers: %#v", users)
		}
	}

	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.updateCalls; got != 1 {
		t.Fatalf("UpdateUsers call count = %d, want 1", got)
	}
	if k.addCalls != 0 || k.removeCalls != 0 {
		t.Fatalf("UUID replacement used incremental calls: add=%d remove=%d", k.addCalls, k.removeCalls)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want rotated UUID", s.lastUsers)
	}
}

func TestApplyUserDeltaUUIDChangeStopsWhenFullUpdateFails(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		updateErr: errors.New("update failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 15, UUID: "uuid-old", SpeedLimit: 4}})

	s.applyUserDelta(
		context.Background(),
		"add",
		[]model.UserSpec{{ID: 15, UUID: "uuid-new", SpeedLimit: 4}},
	)

	if got := k.startCalls; got != 0 {
		t.Fatalf("UUID update triggered restart: calls=%d", got)
	}
	if k.running {
		t.Fatal("kernel remained running after failed UUID update")
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want rotated UUID to remain pending", s.lastUsers)
	}
}

func TestApplyUserDeltaUUIDChangeKeepsFailedState(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		updateErr: errors.New("update failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 15, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	if s.applyUserDelta(
		context.Background(),
		"add",
		[]model.UserSpec{{ID: 15, UUID: "uuid-new", SpeedLimit: 4}},
	) {
		t.Fatal("expected UUID update to fail")
	}

	if got := k.startCalls; got != 0 {
		t.Fatalf("failed UUID update triggered restart: calls=%d", got)
	}
	if k.running {
		t.Fatal("kernel remained running after failed UUID update")
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want new UUID to remain pending", s.lastUsers)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected failed UUID limiter to remain pending")
	}
}

func TestApplyUserDeltaStopsWhenAddAndFullUpdateFail(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		addErr:    errors.New("add failed"),
		updateErr: errors.New("update failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	delta := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.startCalls; got != 0 {
		t.Fatalf("failed add triggered restart: calls=%d", got)
	}
	if k.running {
		t.Fatal("kernel remained running after failed add")
	}
	if len(s.lastUsers) != 2 || s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatalf("failed merged user state was not retained: %#v", s.lastUsers)
	}
}

func TestValidateNodeRuntimeRejectsUnsupportedDNSProvider(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"http"}, &model.NodeSpec{
		Protocol: "http",
		CertConfig: &config.CertConfig{
			CertMode:    "dns",
			DNSProvider: "3123123",
			Domain:      "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
	if got := err.Error(); !strings.HasPrefix(got, `unsupported cert_config.dns_provider "3123123" (supported: `) {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeAllowsSelfManagedTLSBeforeFilesExist(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"anytls", "hysteria"}, &model.NodeSpec{
		Protocol: "anytls",
		CertConfig: &config.CertConfig{
			CertMode: "self",
			Domain:   "example.com",
		},
	}, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("expected self-managed TLS config to pass validation, got %v", err)
	}
}

func TestValidateNodeRuntimeAllowsSingboxRealityWithRequiredFields(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err != nil {
		t.Fatalf("expected sing-box reality validation to pass, got %v", err)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutTLSSettings(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutPrivateKey(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.private_key" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutServerNameOrDest(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.server_name or tls_settings.dest" {
		t.Fatalf("unexpected error: %v", got)
	}
}
