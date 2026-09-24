package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/cert"
	"github.com/P0me1oo/YZ-Agent/internal/cert/dnsproviders"
	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/deviceip"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/singbox"
	"github.com/P0me1oo/YZ-Agent/internal/kernel/xray"
	"github.com/P0me1oo/YZ-Agent/internal/limiter"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/monitor"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/timesync"
	"github.com/P0me1oo/YZ-Agent/internal/tracker"
)

type Service struct {
	cfg          *config.Config
	source       controlplane.Source
	sink         controlplane.Sink
	kernel       kernel.Kernel
	firewall     firewall.Controller
	tracker      *tracker.Tracker
	limiter      *limiter.Limiter
	speedTracker *limiter.SpeedTracker
	cert         *cert.Manager

	// 保存面板最新要求的状态；实际成功运行的状态单独记录在 appliedState。
	lastConfig *model.NodeSpec
	lastUsers  []model.UserSpec
	// 同一份失败配置只尝试一次，配置变化或重新创建 Service 后才允许再启动。
	failedRuntimeHash string
	runtimeError      error

	// nodeLog is the logger with node context for this service instance.
	nodeLog *nlog.NodeLog

	// appliedState tracks the configuration and users that are currently
	// successfully running in the kernel.
	appliedState struct {
		Config *model.NodeSpec
		Users  []model.UserSpec
	}

	pushInterval int // seconds
	pullInterval int // seconds

	lastUserHash   string     // hash of user list for change detection
	lastConfigHash string     // hash of full config for change detection
	pullBackoff    apiBackoff // backoff for panel pull failures
	pushBackoff    apiBackoff // backoff for panel push failures

	// pushActive prevents overlapping push/pull goroutines.
	pushActive atomic.Bool
	pullActive atomic.Bool
	// 关闭时先禁止新报告，再等待已登记的后台报告，避免批次相互覆盖。
	pushMu      sync.Mutex
	pushClosing bool
	pushWG      sync.WaitGroup
	// pullResults delivers async pullViaAPI results back to the main goroutine.
	pullResults chan pullResult

	wsClient         controlplane.PushClient        // Push client (nil if push is not enabled)
	wsEvents         chan controlplane.Event        // receives data events from push transport
	wsStatusCh       chan controlplane.StatusChange // receives push connectivity notifications
	wsCancel         context.CancelFunc             // cancels the WS client goroutine
	wsDisconnectAt   time.Time                      // when WS last disconnected (zero if connected)
	wsResyncPending  atomic.Bool
	machineMailbox   *controlplane.NodeMailbox
	machineMailboxCh <-chan struct{}

	// metricsMu: lastUsers, lastConfig, wsClient, wsDisconnectAt (buildMetrics vs main loop).
	metricsMu sync.RWMutex

	// reportMu 保护失败的报告批次。HTTP 请求可能已到达面板后才断开连接；
	// 保留完整批次和 report ID，可让面板丢弃重试而不丢失请求期间产生的新流量。
	reportMu     sync.Mutex
	retryReport  *reportBatch
	reportBoot   string
	reportSeq    atomic.Uint64
	status       func(RuntimeStatus)
	timeConsumer string
	// certRenewed 默认读取证书管理器；测试可替换以验证轮询失败语义。
	certRenewed func() bool
}

type RuntimeStatus string

const (
	RuntimeStarting RuntimeStatus = "starting"
	RuntimeRunning  RuntimeStatus = "running"
	RuntimeFailed   RuntimeStatus = "failed"
	RuntimeStopped  RuntimeStatus = "stopped"
)

// pullResult carries the outcome of an async pullViaAPI back to the main goroutine.
type pullResult struct {
	config      *model.NodeSpec
	users       []model.UserSpec
	configHash  string
	userHash    string
	certChanged bool
}

type reportBatch struct {
	id      string
	payload controlplane.ReportPayload
}

// apiBackoff implements simple exponential backoff for API failures.
type apiBackoff struct {
	mu            sync.Mutex
	skipRemaining int
}

func (b *apiBackoff) shouldSkip() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.skipRemaining > 0 {
		b.skipRemaining--
		return true
	}
	return false
}

func (b *apiBackoff) onSuccess() {
	b.mu.Lock()
	b.skipRemaining = 0
	b.mu.Unlock()
}

func (b *apiBackoff) onFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.skipRemaining <= 0 {
		b.skipRemaining = 1
	} else if b.skipRemaining < 8 {
		b.skipRemaining *= 2
	}
}

func New(cfg *config.Config) *Service {
	var cp controlplane.ControlPlane
	if cfg.IsStandalone() {
		cp = controlplane.NewLocalControlPlane(cfg)
	} else {
		cp = controlplane.NewPanelControlPlane(cfg.Panel, cfg.WS)
	}
	return newService(cfg, cp)
}

// NewWithControlPlane creates a Service with an externally-provided
// ControlPlane. Used by the machine orchestrator to inject a
// MachinePanelControlPlane with WS mux routing.
func NewWithControlPlane(cfg *config.Config, cp controlplane.ControlPlane) *Service {
	return newService(cfg, cp)
}

// SetStatusHandler registers a lifecycle observer before Run is called.
func (s *Service) SetStatusHandler(handler func(RuntimeStatus)) {
	s.status = handler
}

func (s *Service) notifyStatus(status RuntimeStatus) {
	if s.status != nil {
		s.status(status)
	}
}

func newService(cfg *config.Config, cp controlplane.ControlPlane) *Service {
	certMgr := cert.NewManager(cfg.Cert)

	var k kernel.Kernel
	switch cfg.Kernel.Type {
	case "singbox":
		k = singbox.New(cfg.Kernel)
	case "xray":
		k = xray.New(cfg.Kernel)
	default:
		nlog.Core().Warn("unsupported kernel type, defaulting to Xray", "type", cfg.Kernel.Type)
		k = xray.New(cfg.Kernel)
	}

	l := limiter.New()
	st := limiter.NewSpeedTracker(l)

	return &Service{
		cfg:          cfg,
		source:       cp,
		sink:         cp,
		kernel:       k,
		tracker:      tracker.New(),
		limiter:      l,
		speedTracker: st,
		cert:         certMgr,
		wsEvents:     make(chan controlplane.Event, 16),
		wsStatusCh:   make(chan controlplane.StatusChange, 4),
		pullResults:  make(chan pullResult, 1),
		reportBoot:   newReportBootID(),
		timeConsumer: fmt.Sprintf("%s/node/%d", cfg.InstanceID, cfg.Panel.NodeID),
	}
}

func newReportBootID() string {
	buf := make([]byte, 12)
	if _, err := cryptorand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	// 该回退值只用于进程内重试流标识，不是认证值，仅在系统随机源不可用时使用。
	return fmt.Sprintf("%x", time.Now().UnixNano())
}

func (s *Service) Run(ctx context.Context) (runErr error) {
	s.notifyStatus(RuntimeStarting)
	defer timesync.Default().SetUsage(s.timeConsumer, false)
	defer func() {
		if runErr != nil {
			s.notifyStatus(RuntimeFailed)
			return
		}
		s.notifyStatus(RuntimeStopped)
	}()

	// 证书与节点配置一起应用，失败时保留控制通道接收修正。
	defer s.cert.Stop()
	defer s.stopKernel()

	// Handshake: get WS config + initial data in one call
	if err := s.initialSetup(ctx); err != nil {
		return fmt.Errorf("initial setup: %w", err)
	}

	// Set up tickers
	trackTicker := time.NewTicker(time.Duration(s.cfg.Node.TrackInterval) * time.Second)
	pushInterval := time.Duration(math.Max(float64(s.pushInterval), 5)) * time.Second
	pullInterval := time.Duration(s.pullInterval) * time.Second
	wsReconcileInterval := max(pullInterval, 5*time.Minute)
	lastWSReconcile := time.Now()
	reportTicker := time.NewTicker(pushInterval)
	pullTicker := time.NewTicker(pullInterval)
	deviceReportTicker := time.NewTicker(time.Duration(s.cfg.Node.DeviceReportInterval) * time.Second)

	// WS discovery: when in REST-only mode, periodically re-handshake to check
	// if WS has been enabled. When WS is disconnected for too long, re-check
	// if it's still available.
	wsDiscoveryTicker := time.NewTicker(time.Duration(s.cfg.WS.DiscoveryInterval) * time.Second)

	defer trackTicker.Stop()
	defer reportTicker.Stop()
	defer pullTicker.Stop()
	defer deviceReportTicker.Stop()
	defer wsDiscoveryTicker.Stop()

	s.startWSClient(ctx)
	if s.runtimeError == nil {
		s.notifyStatus(RuntimeRunning)
	}

	for {
		select {
		case <-ctx.Done():
			s.stopKernel()
			s.pushReportSync()
			return nil

		case <-trackTicker.C:
			s.trackAndEnforce(ctx)

		case <-reportTicker.C:
			s.pushReportAsync()

		case <-deviceReportTicker.C:
			s.reportDevices()

		case <-pullTicker.C:
			if s.wsClient != nil && s.wsClient.IsConnected() {
				if time.Since(lastWSReconcile) < wsReconcileInterval {
					continue
				}
				lastWSReconcile = time.Now()
				nlog.Core().Debug("periodic REST reconciliation (ws connected)")
			} else {
				nlog.Core().Debug("polling from API (ws not connected)")
			}
			s.pullViaAPIAsync(ctx)

		case result := <-s.pullResults:
			s.applyPullResult(ctx, result)

		case <-wsDiscoveryTicker.C:
			s.wsDiscovery(ctx)

		case status := <-s.wsStatusCh:
			s.handleWSStatus(ctx, status)

		case <-s.machineMailboxCh:
			s.drainMachineMailbox(ctx)

		case event := <-s.wsEvents:
			s.handleWSEvent(ctx, event)
		}
	}
}

func (s *Service) initialSetup(ctx context.Context) error {
	// Register speed limit lookup with kernel unconditionally (before push/poll branch).
	s.kernel.SetSpeedLimitFunc(s.speedTracker.GetLimiter)
	s.kernel.SetDeviceLimitFunc(s.limiter.GetDeviceLimitByUUID)
	s.kernel.SetConnLimiter(s.limiter)

	bootstrap, err := s.source.Initial(ctx, s.wsMetrics, s.wsEvents, s.wsStatusCh)
	if err != nil {
		return err
	}

	if s.cfg.Node.PushInterval == 0 && bootstrap.PushInterval > 0 {
		s.pushInterval = bootstrap.PushInterval
	} else {
		s.pushInterval = s.cfg.Node.PushInterval
	}
	if s.pushInterval == 0 {
		s.pushInterval = 60
	}

	if s.cfg.Node.PullInterval == 0 && bootstrap.PullInterval > 0 {
		s.pullInterval = bootstrap.PullInterval
	} else {
		s.pullInterval = s.cfg.Node.PullInterval
	}
	if s.pullInterval == 0 {
		s.pullInterval = 60
	}

	if bootstrap.Push != nil {
		s.wsClient = bootstrap.Push
	}
	s.machineMailbox = bootstrap.Mailbox
	if s.machineMailbox != nil {
		s.machineMailboxCh = s.machineMailbox.NotifyCh()
	}
	if bootstrap.Config == nil {
		if bootstrap.Push != nil {
			// In machine mode a shared WS client may be available before the first
			// per-node snapshot arrives. In that case we wait for subsequent WS/REST
			// updates instead of failing startup.
			return nil
		}
		return fmt.Errorf("initial config is nil")
	}
	s.metricsMu.Lock()
	s.lastConfig = bootstrap.Config
	s.metricsMu.Unlock()
	applyDeviceIPExclude(bootstrap.Config)
	s.lastConfigHash = computeConfigHash(bootstrap.Config)
	s.updateUserState(bootstrap.Users)

	nlog.Core().Info("initial snapshot ready",
		"protocol", bootstrap.Config.Protocol,
		"port", bootstrap.Config.ServerPort,
		"users", len(bootstrap.Users),
	)

	// 应用失败由节点自身记录并停止内核；初始同步仍完成，继续等待面板修正。
	s.applyChanges(ctx, true, false)
	s.markMailboxReadyAndDrain(ctx)
	return nil
}

// applyRemoteOverrides updates service-level settings (log level, cert config)
// from the panel's NodeConfig. The bool reports whether cert paths changed;
// an error means the remote setting was not applied and the runtime must stop.
func (s *Service) applyRemoteOverrides(ctx context.Context, nc *model.NodeSpec) (bool, error) {
	if nc == nil {
		return false, nil
	}

	// Dynamic Log Level (Kernel)
	if nc.KernelLogLevel != "" && nc.KernelLogLevel != s.cfg.Kernel.LogLevel {
		nlog.Core().Info("cert: kernel log level override", "old", s.cfg.Kernel.LogLevel, "new", nc.KernelLogLevel)
		s.cfg.Kernel.LogLevel = nc.KernelLogLevel
	}

	// Certificate configuration from panel (panel-first: takes precedence over local config)
	if nc.CertConfig != nil {
		return s.applyNodeCert(ctx, nc.CertConfig)
	}

	// Legacy fields (deprecated: prefer cert_config)
	if nc.AutoTLS != s.cfg.Cert.AutoTLS {
		nlog.Core().Info("cert: auto_tls policy changed (deprecated field)", "new", nc.AutoTLS)
		s.cfg.Cert.AutoTLS = nc.AutoTLS
	}
	if nc.Domain != "" && nc.Domain != s.cfg.Cert.Domain {
		s.cfg.Cert.Domain = nc.Domain
	}

	return false, nil
}

// applyPanelCert converts a panel CertConfig into the local config format and
// reconfigures the cert manager. Reports whether cert paths changed.
func (s *Service) applyNodeCert(ctx context.Context, newCfg *config.CertConfig) (bool, error) {
	if newCfg == nil {
		return false, nil
	}
	cfgCopy := *newCfg
	cfgCopy.CertDir = s.cfg.Cert.CertDir

	changed, err := s.cert.Reconfigure(ctx, cfgCopy)
	if err != nil {
		nlog.Core().Error("failed to apply runtime cert config", "mode", cfgCopy.CertMode, "error", err)
		return false, fmt.Errorf("runtime certificate configuration: %w", err)
	}
	s.cfg.Cert = cfgCopy
	if changed {
		msg := fmt.Sprintf("cert: material updated, has_cert=%v", s.cert.HasCert())
		if s.nodeLog != nil {
			s.nodeLog.Info(msg)
		} else {
			nlog.Core().Info(msg)
		}
	}
	return changed, nil
}

// startWSClient starts the push client goroutine if a client is configured.
func (s *Service) startWSClient(ctx context.Context) {
	if s.wsClient == nil {
		return
	}
	wsCtx, wsCancel := context.WithCancel(ctx)
	s.wsCancel = wsCancel
	go s.wsClient.Run(wsCtx)
}

func (s *Service) markMailboxReadyAndDrain(ctx context.Context) {
	if s.machineMailbox == nil {
		return
	}
	// Seed mailbox with bootstrap state so delta events can be applied
	// incrementally instead of always triggering REST reconciliation.
	s.metricsMu.RLock()
	users := s.lastUsers
	config := s.lastConfig
	s.metricsMu.RUnlock()
	s.machineMailbox.SeedBaseline(users, config)
	s.machineMailbox.MarkReady()
	s.drainMachineMailbox(ctx)
}

func (s *Service) drainMachineMailbox(ctx context.Context) {
	if s.machineMailbox == nil {
		return
	}
	state := s.machineMailbox.DrainIfReady()
	if state.HasConfig {
		s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncConfig, Config: state.Config})
	}
	if state.HasUsers {
		s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncUsers, Users: state.Users})
	}
	if state.HasDevices {
		s.handleWSEvent(ctx, controlplane.Event{Type: controlplane.EventSyncDevices, DeviceUsers: state.DeviceUsers})
	}
	if state.NeedsReconcile {
		s.requestWSResync(ctx, "machine_mailbox_reconcile")
	}
}

func (s *Service) requestWSResync(ctx context.Context, reason string) {
	if !s.wsResyncPending.CompareAndSwap(false, true) {
		return
	}
	if s.nodeLog != nil {
		s.nodeLog.Warn("ws state may be stale, scheduling REST reconciliation", "reason", reason)
	} else {
		nlog.Core().Warn("ws state may be stale, scheduling REST reconciliation", "reason", reason)
	}
	s.pullViaAPIAsync(ctx)
}

func (s *Service) wsMetrics() map[string]interface{} {
	status := monitor.Collect()
	m := s.buildMetrics(status)
	m["kernel_status"] = s.kernel.IsRunning()
	return m
}

// handleWSStatus reacts to WS connectivity changes.

// - On disconnect: record timestamp, immediately REST poll.
// - On reconnect: clear disconnect timestamp, REST poll to catch missed events.
func (s *Service) handleWSStatus(ctx context.Context, status controlplane.StatusChange) {
	if status.NeedsResync {
		s.requestWSResync(ctx, "drop_detected")
	}
	if status.Connected {
		s.metricsMu.Lock()
		s.wsDisconnectAt = time.Time{}
		s.metricsMu.Unlock()
		// Use nodeLog if available, otherwise core
		if s.nodeLog != nil {
			s.nodeLog.Info("ws connected")
		} else {
			nlog.Core().Info("ws connected")
		}
		// After reconnect, proactively pull once to ensure we haven't missed
		// any updates during the disconnection window.
		s.pullViaAPIAsync(ctx)
	} else {
		s.metricsMu.Lock()
		if s.wsDisconnectAt.IsZero() {
			s.wsDisconnectAt = time.Now()
		}
		s.metricsMu.Unlock()
		if s.nodeLog != nil {
			s.nodeLog.Info("ws disconnected")
		} else {
			nlog.Core().Info("ws disconnected")
		}
		// Clear global device state on disconnect
		s.kernel.ClearGlobalDevices()
		s.pullViaAPIAsync(ctx)
	}
}

// wsDiscovery periodically checks WS availability:
//
//  1. REST-only mode (wsClient == nil): Re-handshake to check if panel now has
//     WS enabled. If so, create and start a WS client. This handles the case
//     where WS was not enabled at startup but enabled later.
//
//  2. WS disconnected for >10 min: Re-handshake to check if WS config changed.
//     If WS is now disabled, stop the WS client and switch to REST-only.
//     If WS config changed (different URL/channel), restart with new config.
func (s *Service) wsDiscovery(ctx context.Context) {
	if !s.source.SupportsDiscovery() {
		return
	}

	needsCheck := false
	if s.wsClient == nil {
		needsCheck = true
		nlog.Core().Debug("push discovery: no push client, checking if control plane enabled push")
	} else if !s.wsDisconnectAt.IsZero() && time.Since(s.wsDisconnectAt) > 10*time.Minute {
		needsCheck = true
		nlog.Core().Debug("push discovery: push disconnected for >10min, re-checking")
	}
	if !needsCheck {
		return
	}

	pushClient, err := s.source.Discover(ctx, s.wsMetrics, s.wsEvents, s.wsStatusCh)
	if err != nil {
		nlog.Core().Debug("push discovery failed", "error", err)
		return
	}
	if s.source.SupportsPolling() {
		s.pullViaAPIAsync(ctx)
	}

	if pushClient != nil {
		if s.wsClient == nil {
			nlog.Core().Info("push discovery: control plane enabled push, creating client")
			s.metricsMu.Lock()
			s.wsClient = pushClient
			s.wsDisconnectAt = time.Time{}
			s.metricsMu.Unlock()
			s.startWSClient(ctx)
		}
	} else if s.wsClient != nil {
		nlog.Core().Info("push discovery: control plane disabled push, switching to polling")
		if s.wsCancel != nil {
			s.wsCancel()
		}
		s.metricsMu.Lock()
		s.wsClient = nil
		s.wsDisconnectAt = time.Time{}
		s.metricsMu.Unlock()
		s.wsCancel = nil
	}
}

// handleWSEvent processes data events received via WebSocket
func (s *Service) handleWSEvent(ctx context.Context, event controlplane.Event) {
	switch event.Type {
	case controlplane.EventSyncConfig:
		if event.Config == nil {
			return
		}
		applyDeviceIPExclude(event.Config)
		newConfigHash := computeConfigHash(event.Config)
		if newConfigHash == s.lastConfigHash {
			return
		}
		if !s.applyConfigUpdate(ctx, event.Config, newConfigHash) {
			s.resetPollingState()
		}

	case controlplane.EventSyncUsers:
		if event.Users == nil {
			return
		}
		newHash := computeUserHash(event.Users)
		if newHash == s.lastUserHash {
			return
		}
		if s.nodeLog != nil {
			s.nodeLog.Info(fmt.Sprintf("users updated, %d users", len(event.Users)))
		}
		if !s.applyUserUpdate(ctx, event.Users, newHash) {
			s.resetPollingState()
		}

	case controlplane.EventSyncUserDelta:
		if len(event.DeltaUsers) == 0 {
			return
		}
		if s.nodeLog != nil {
			s.nodeLog.Info(fmt.Sprintf("users delta: %s, %d users", event.DeltaAction, len(event.DeltaUsers)))
		}
		if !s.applyUserDelta(ctx, event.DeltaAction, event.DeltaUsers) {
			s.resetPollingState()
		}

	case controlplane.EventSyncDevices:
		// Sync global device state
		if event.DeviceUsers != nil {
			s.kernel.UpdateGlobalDevices(event.DeviceUsers)
		}

	default:
		nlog.Core().Debug(fmt.Sprintf("unknown ws event: %v", event.Type))
	}
}

// pullViaAPIAsync fetches config/users from the panel API in a background
// goroutine and sends the result to pullResults for the main goroutine to apply.
func (s *Service) pullViaAPIAsync(ctx context.Context) {
	if !s.source.SupportsPolling() {
		return
	}
	if !s.pullActive.CompareAndSwap(false, true) {
		nlog.Core().Debug("pull already in progress, skipping")
		return
	}
	if s.pullBackoff.shouldSkip() {
		nlog.Core().Debug("skipping pull due to backoff")
		s.pullActive.Store(false)
		return
	}

	currentConfigHash := s.lastConfigHash

	go func() {
		defer s.pullActive.Store(false)
		snapshot, err := s.source.Poll(ctx)
		if err != nil {
			nlog.Core().Error("poll control plane failed", "error", err)
			s.pullBackoff.onFailure()
			return
		}
		s.pullBackoff.onSuccess()

		// 只有 REST 请求成功后才消费证书续期标记。请求失败时保留该标记，
		// 让下一次对账仍能触发内核重载；请求期间新发生的续期也会被本次结果带上。
		takeCertRenewal := s.certRenewed
		if takeCertRenewal == nil {
			takeCertRenewal = s.cert.CertRenewed
		}
		result := pullResult{certChanged: takeCertRenewal()}
		if snapshot.Config != nil {
			// 名单不参与配置哈希；配置未变时 result.config 会被丢弃，所以在这里先应用。
			applyDeviceIPExclude(snapshot.Config)
			result.config = snapshot.Config
			result.configHash = computeConfigHash(snapshot.Config)
			if result.configHash == currentConfigHash && !result.certChanged {
				result.config = nil
			}
		}
		if snapshot.Users != nil {
			result.users = snapshot.Users
			result.userHash = computeUserHash(snapshot.Users)
		}

		select {
		case s.pullResults <- result:
		case <-ctx.Done():
		}
	}()
}

// applyPullResult processes the result of an async pullViaAPI on the main goroutine.
func (s *Service) applyPullResult(ctx context.Context, result pullResult) {
	s.wsResyncPending.Store(false)

	needsReload := result.config != nil || result.certChanged
	usersChanged := result.users != nil && result.userHash != s.lastUserHash
	if !needsReload {
		if usersChanged {
			if !s.applyUserUpdate(ctx, result.users, result.userHash) {
				s.resetPollingState()
			}
		}
		return
	}

	if result.certChanged {
		nlog.Core().Info("certificate renewed, kernel restart needed")
	}

	if usersChanged {
		s.prepareUserState(result.users)
	}

	applied := false
	if result.config != nil {
		applied = s.applyConfigUpdate(ctx, result.config, result.configHash)
	} else {
		applied = s.applyChanges(ctx, true, false)
	}
	if !applied {
		s.resetPollingState()
		return
	}
	if usersChanged && !s.lastConfig.IsRelayLanding() {
		s.lastUserHash = result.userHash
	}
}

// ─── User state helpers ─────────────────────────────────────────────────────

func (s *Service) updateUserState(users []model.UserSpec) {
	if s.lastConfig.IsRelayLanding() {
		users = nil
	}
	if users == nil {
		users = []model.UserSpec{}
	}
	s.prepareUserState(users)
}

func (s *Service) prepareUserState(users []model.UserSpec) {
	if users == nil {
		users = []model.UserSpec{}
	}

	s.limiter.UpdateUsers(users)
	s.speedTracker.UpdateBuckets()

	s.metricsMu.Lock()
	s.lastUsers = append([]model.UserSpec{}, users...)
	s.metricsMu.Unlock()
	s.lastUserHash = computeUserHash(users)
}

// applyConfigUpdate 先把配置作为待应用状态交给内核。
// 无论成功还是失败，lastConfig 都保留面板最新要求；失败时节点保持停止，
// 这样后续用户同步不会误把旧配置重新启动起来。
func (s *Service) applyConfigUpdate(ctx context.Context, config *model.NodeSpec, hash string) bool {
	s.metricsMu.Lock()
	s.lastConfig = config
	s.metricsMu.Unlock()
	s.lastConfigHash = hash

	if !s.applyChanges(ctx, true, false) {
		// 失败配置仍作为当前期望状态保留，避免任何后台路径拉起旧配置。
		return false
	}

	if s.nodeLog == nil {
		s.nodeLog = nlog.ForNode(config.Protocol, config.ServerPort)
	}
	s.nodeLog.Info(fmt.Sprintf("config updated, %d users", len(s.lastUsers)))
	return true
}

func (s *Service) setTimeUsage(config *model.NodeSpec) {
	timesync.Default().SetUsage(s.timeConsumer, config != nil && config.UsesSS2022())
}

func (s *Service) resetPollingState() {
	if resetter, ok := s.source.(controlplane.PollStateResetter); ok {
		resetter.ResetPollingState()
	}
}

// startKernel 对每次启动执行完整检查，包括由用户变化触发的故障恢复。
func (s *Service) startKernel(ctx context.Context, nc *model.NodeSpec, users []model.UserSpec) bool {
	if !s.prepareRuntime(ctx, nc, users) {
		return false
	}
	if err := s.kernel.Start(nc, users, s.tlsCert()); err != nil {
		s.failRuntime("启动内核失败", nc, users, err)
		return false
	}
	if !s.applyFirewall(ctx, nc, users) {
		return false
	}

	s.runtimeApplied(nc, users)

	// Initialize node logger on first successful start
	if s.nodeLog == nil {
		s.nodeLog = nlog.ForNode(nc.Protocol, nc.ServerPort)
	}
	s.speedTracker.SetLogCallback(func(msg string) {
		fullMsg := fmt.Sprintf("speedtracker: %s active_limiters=%d", msg, s.speedTracker.LimitedUserCount())
		s.nodeLog.Info(fullMsg)
	})
	s.nodeLog.Info(fmt.Sprintf("started, %d users", len(users)))
	return true
}

// ensureRunning starts the kernel if it is not running and there are users +
// config available. Returns true if the kernel is running afterwards.
func (s *Service) ensureRunning(ctx context.Context) bool {
	if s.kernel.IsRunning() {
		return true
	}
	if s.lastConfig == nil || !s.applyChanges(ctx, true, false) {
		return false
	}
	return s.kernel.IsRunning()
}

// ─── User update entry points ───────────────────────────────────────────────

// applyUserUpdate replaces the full user set and hot-swaps the kernel.
// Called from WS sync.users and REST polling.
func (s *Service) applyUserUpdate(ctx context.Context, users []model.UserSpec, newHash string) bool {
	if s.lastConfig.IsRelayLanding() {
		return s.syncLandingUserState(ctx)
	}
	// 先保存最新用户状态，再尝试启动内核。否则内核停止时
	// ensureRunning 只能看到旧的空用户列表，会把首个用户更新丢掉。
	wasRunning := s.kernel.IsRunning()
	s.prepareUserState(users)
	if !s.ensureRunning(ctx) {
		// 没有用户时可以保持空闲；配置或证书仍错误时继续报告失败。
		return s.runtimeError == nil
	}
	if !wasRunning {
		if newHash != "" {
			s.lastUserHash = newHash
		}
		return true
	}

	added, removed, err := s.kernel.UpdateUsers(users)
	if err != nil {
		s.failRuntime("更新用户失败", s.lastConfig, users, err)
		return false
	}
	s.appliedState.Users = s.lastUsers
	if newHash != "" {
		s.lastUserHash = newHash
	}
	if s.nodeLog != nil && (added > 0 || removed > 0) {
		s.nodeLog.Info(fmt.Sprintf("users updated: +%d -%d", added, removed))
	}
	return true
}

// applyUserDelta applies an incremental user change (add or remove) directly
// via the kernel's atomic user API. The latest state is prepared first so a
// stopped kernel can start as soon as the first user arrives.
func (s *Service) applyUserDelta(ctx context.Context, action string, deltaUsers []model.UserSpec) bool {
	if s.lastConfig.IsRelayLanding() && (action == "add" || action == "remove") {
		return s.syncLandingUserState(ctx)
	}
	switch action {
	case "add":
		// Defensive check for empty or nil deltaUsers
		if deltaUsers == nil || len(deltaUsers) == 0 {
			return true
		}
		uuidChange := hasUUIDChange(s.lastUsers, deltaUsers)
		merged := mergeUsers(s.lastUsers, deltaUsers)
		// UUID 变更走完整替换；失败时保留最新身份，不恢复旧用户。
		if uuidChange {
			return s.applyUserUpdate(ctx, merged, computeUserHash(merged))
		}
		wasRunning := s.kernel.IsRunning()
		s.prepareUserState(merged)

		if !s.ensureRunning(ctx) {
			return s.runtimeError == nil
		}
		if !wasRunning {
			return true
		}

		added, err := s.kernel.AddUsers(deltaUsers)
		if err != nil {
			nlog.Core().Error("新增用户热更新失败，尝试完整用户更新", "error", err)
			if _, _, err := s.kernel.UpdateUsers(merged); err != nil {
				s.failRuntime("新增用户失败", s.lastConfig, merged, err)
				return false
			}
		}
		if s.nodeLog != nil && added > 0 {
			s.nodeLog.Info(fmt.Sprintf("users added: +%d", added))
		}

	case "remove":
		// Defensive check for empty or nil deltaUsers
		if deltaUsers == nil || len(deltaUsers) == 0 {
			return true
		}
		filtered := subtractUsers(s.lastUsers, deltaUsers)
		s.prepareUserState(filtered)
		if !s.kernel.IsRunning() {
			// 删除也可能修正失败快照，剩余用户应立即获得一次新的启动机会。
			return s.applyChanges(ctx, true, false)
		}

		removed, err := s.kernel.RemoveUsers(deltaUsers)
		if err != nil {
			nlog.Core().Error("删除用户热更新失败，尝试完整用户更新", "error", err)
			if _, _, err := s.kernel.UpdateUsers(filtered); err != nil {
				s.failRuntime("删除用户失败", s.lastConfig, filtered, err)
				return false
			}
		}
		if s.nodeLog != nil && removed > 0 {
			s.nodeLog.Info(fmt.Sprintf("users removed: -%d", removed))
		}

	default:
		nlog.Core().Warn(fmt.Sprintf("unknown user delta action: %s", action))
		return false
	}
	s.appliedState.Users = s.lastUsers
	return true
}

// 落地的用户消息只清理普通用户状态；恢复已停止的内核仍须通过原有配置校验。
func (s *Service) syncLandingUserState(ctx context.Context) bool {
	s.prepareUserState(nil)
	if !s.ensureRunning(ctx) {
		return false
	}
	s.appliedState.Users = s.lastUsers
	return true
}

// hasUUIDChange 识别增量消息中需要完整替换而不是单纯追加的凭据变更。
func hasUUIDChange(base, delta []model.UserSpec) bool {
	current := make(map[int]string, len(base))
	for _, user := range base {
		current[user.ID] = user.UUID
	}
	for _, user := range delta {
		if uuid, exists := current[user.ID]; exists && uuid != user.UUID {
			return true
		}
	}
	return false
}

// mergeUsers overlays deltaUsers onto base (keyed by ID). New users are
// appended, existing users have their properties overwritten.
func mergeUsers(base, delta []model.UserSpec) []model.UserSpec {
	// Handle nil slices
	if base == nil {
		base = []model.UserSpec{}
	}
	if delta == nil {
		return base
	}

	m := make(map[int]model.UserSpec, len(base))
	for _, u := range base {
		m[u.ID] = u
	}
	for _, u := range delta {
		m[u.ID] = u
	}
	out := make([]model.UserSpec, 0, len(m))
	for _, u := range m {
		out = append(out, u)
	}
	return out
}

// subtractUsers returns base with all users in delta removed.
func subtractUsers(base, delta []model.UserSpec) []model.UserSpec {
	if base == nil {
		return nil
	}
	if delta == nil || len(delta) == 0 {
		return base
	}
	removeSet := make(map[int]struct{}, len(delta))
	for _, u := range delta {
		removeSet[u.ID] = struct{}{}
	}
	out := make([]model.UserSpec, 0, len(base))
	for _, u := range base {
		if _, ok := removeSet[u.ID]; !ok {
			out = append(out, u)
		}
	}
	return out
}

// applyChanges applies config changes to the kernel. User-only changes are
// handled by applyUserUpdate/applyUserDelta directly via the atomic user API.
func (s *Service) applyChanges(ctx context.Context, configChanged, usersChanged bool) bool {
	if !configChanged || s.lastConfig == nil {
		return true
	}

	// A landing node serves only the internal transit inbound, so an empty user
	// set is its normal state and must not shut the kernel down.
	isLanding := s.lastConfig.IsRelayLanding()
	if isLanding {
		// 配置与用户同批到达、证书重载时也不能把普通用户带入落地。
		s.prepareUserState(nil)
	}

	if !s.kernel.IsRunning() && (len(s.lastUsers) > 0 || isLanding) {
		return s.startKernel(ctx, s.lastConfig, s.lastUsers)
	}
	if !s.prepareRuntime(ctx, s.lastConfig, s.lastUsers) {
		return false
	}
	if len(s.lastUsers) == 0 && !isLanding {
		s.stopKernel()
		s.runtimeApplied(nil, nil)
		return true
	}

	// If config changed, delegate to kernel.Reload. The kernel implementation
	// decides whether to hot-swap users, reconstruct inbounds, or restart itself.
	if err := s.kernel.Reload(s.lastConfig, s.lastUsers, s.tlsCert()); err != nil {
		s.failRuntime("重载内核失败", s.lastConfig, s.lastUsers, err)
		return false
	}
	if !s.applyFirewall(ctx, s.lastConfig, s.lastUsers) {
		return false
	}
	s.runtimeApplied(s.lastConfig, s.lastUsers)
	if s.nodeLog != nil {
		s.nodeLog.Info(fmt.Sprintf("config updated, %d users", len(s.lastUsers)))
	}
	return true
}

func (s *Service) trackAndEnforce(ctx context.Context) {
	if s.appliedState.Config != nil && !s.kernel.IsRunning() {
		s.failRuntime("内核意外停止", s.lastConfig, s.lastUsers, fmt.Errorf("内核已不在运行"))
	}
	connCount, userCount, err := s.collectTraffic(ctx)
	if err != nil {
		nlog.Core().Debug("get user traffic failed", "error", err)
		return
	}

	// Only log stats if there's actual traffic or connections
	if connCount > 0 || userCount > 0 {
		if s.nodeLog != nil {
			s.nodeLog.Debug(fmt.Sprintf("tracker: %d conns, %d users online", connCount, userCount))
		} else {
			nlog.TrackerStats(connCount, userCount)
		}
	}
}

// collectTraffic 只采样，不启动内核；停止节点和进程退出也需要读取最终累计值。
func (s *Service) collectTraffic(ctx context.Context) (connCount, userCount int, err error) {
	traffic, aliveIPs, connCount, err := s.kernel.GetUserTraffic(ctx)
	if err != nil {
		return 0, 0, err
	}
	s.tracker.Process(traffic, aliveIPs, connCount)
	s.trackRelayTraffic(ctx)
	return connCount, len(traffic), nil
}

// pushReportAsync 在后台发送报告，避免慢 HTTP 阻塞主循环；同一时间只允许一个推送。
func (s *Service) pushReportAsync() {
	if !s.sink.SupportsReporting() {
		return
	}
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.pushClosing {
		return
	}
	if !s.pushActive.CompareAndSwap(false, true) {
		nlog.Core().Debug("push already in progress, skipping")
		return
	}
	if s.pushBackoff.shouldSkip() {
		nlog.Core().Debug("skipping report due to backoff")
		s.pushActive.Store(false)
		return
	}

	batch := s.takeReportBatch()
	s.pushWG.Add(1)

	go func() {
		defer s.pushWG.Done()
		defer s.pushActive.Store(false)
		if err := s.sink.Report(batch.payload); err != nil {
			nlog.Core().Warn("failed to push report", "error", err)
			s.rememberFailedReport(batch)
			s.pushBackoff.onFailure()
			return
		}
		s.forgetCompletedReport(batch)
		s.pushBackoff.onSuccess()
		nlog.ReportPushed(len(batch.payload.Traffic), len(batch.payload.Online))
	}()
}

// pushReportSync 等待在途报告并补采停止后的累计计数，先重试旧批次再刷出最后增量。
func (s *Service) pushReportSync() {
	if !s.sink.SupportsReporting() {
		return
	}
	s.pushMu.Lock()
	s.pushClosing = true
	s.pushMu.Unlock()
	s.pushWG.Wait()

	// 不调用 trackAndEnforce，避免退出采样把已经停止的内核重新启动。
	if _, _, err := s.collectTraffic(context.Background()); err != nil {
		nlog.Core().Warn("failed to collect final traffic", "error", err)
	}

	s.reportMu.Lock()
	hasRetry := s.retryReport != nil
	s.reportMu.Unlock()
	batchCount := 1
	if hasRetry {
		batchCount++
	}
	for range batchCount {
		batch := s.takeReportBatch()
		if err := s.sink.Report(batch.payload); err != nil {
			nlog.Core().Warn("failed to push final report", "error", err)
			s.rememberFailedReport(batch)
			return
		}
		s.forgetCompletedReport(batch)
	}
}

// takeReportBatch 优先返回失败批次。新产生的数据留在 tracker 中，直到旧批次成功，
// 因而重试不会在同一 ID 下混入已经接受过的新流量。
func (s *Service) takeReportBatch() *reportBatch {
	s.reportMu.Lock()
	if s.retryReport != nil {
		batch := s.retryReport
		s.reportMu.Unlock()
		return batch
	}
	s.reportMu.Unlock()

	traffic := cloneTraffic(s.tracker.FlushTraffic())
	relayTraffic := cloneTraffic(s.tracker.FlushRelayTraffic())
	relayUserTraffic := cloneRelayUserTraffic(s.tracker.FlushRelayUserTraffic())
	aliveIPs := cloneAliveIPs(s.tracker.FlushAliveIPs())
	status := monitor.Collect()
	metrics := s.buildMetrics(status)
	metrics["kernel_status"] = s.kernel.IsRunning()
	limitEvents := s.collectLimitEvents()
	reportID := s.nextReportID()

	return &reportBatch{
		id: reportID,
		payload: controlplane.ReportPayload{
			ReportID:         reportID,
			Traffic:          traffic,
			RelayTraffic:     relayTraffic,
			RelayUserTraffic: relayUserTraffic,
			Alive:            aliveIPs,
			Online:           s.tracker.CurrentOnline(),
			CPU:              status.CPU,
			Mem:              [2]uint64{status.MemTotal, status.MemUsed},
			Swap:             [2]uint64{status.SwapTotal, status.SwapUsed},
			Disk:             [2]uint64{status.DiskTotal, status.DiskUsed},
			Metrics:          metrics,
			LimitEvents:      limitEvents,
		},
	}
}

// collectLimitEvents 取出本周期的连接超限统计并转成上报格式。
// 快照即清零，所以只能在组装批次时调用一次；上报失败时批次会整体重试，
// 事件跟着批次走，不会丢。
func (s *Service) collectLimitEvents() []panel.LimitEvent {
	stats := s.limiter.SnapshotLimitEvents()
	if len(stats) == 0 {
		return nil
	}
	events := make([]panel.LimitEvent, 0, len(stats))
	for userID, stat := range stats {
		if stat.ConnHits > 0 {
			events = append(events, panel.LimitEvent{
				UserID:   userID,
				Kind:     model.ConnLimitKindConcurrent,
				Limit:    stat.ConnLimit,
				Observed: stat.PeakConn,
				Count:    stat.ConnHits,
			})
		}
		if stat.RateHits > 0 {
			events = append(events, panel.LimitEvent{
				UserID: userID,
				Kind:   model.ConnLimitKindRate,
				Limit:  stat.RateLimit,
				Count:  stat.RateHits,
			})
		}
		if stat.DeviceHits > 0 {
			events = append(events, panel.LimitEvent{
				UserID:   userID,
				Kind:     model.ConnLimitKindDevice,
				Limit:    stat.DeviceLimit,
				Observed: stat.PeakDevices,
				Count:    stat.DeviceHits,
				IPs:      stat.DeviceIPs,
			})
		}
	}
	// 用户 ID 顺序固定，便于面板日志和重试比对。
	sort.Slice(events, func(i, j int) bool {
		if events[i].UserID != events[j].UserID {
			return events[i].UserID < events[j].UserID
		}
		return events[i].Kind < events[j].Kind
	})
	return events
}

// trackRelayTraffic 从入口内部出站采集按逻辑节点统计的中转流量；不支持该能力的内核跳过。
func (s *Service) trackRelayTraffic(ctx context.Context) {
	// 内核可能仍在结算上一个中转配置，不能只按当前节点角色过滤。
	reader, ok := s.kernel.(kernel.RelayTrafficReader)
	if !ok {
		return
	}
	relay, err := reader.GetRelayTraffic(ctx)
	if err != nil {
		nlog.Core().Debug("get relay traffic failed", "error", err)
		return
	}
	s.tracker.ProcessRelay(relay)

	userReader, ok := s.kernel.(kernel.RelayUserTrafficReader)
	if !ok {
		return
	}
	relayUser, err := userReader.GetRelayUserTraffic(ctx)
	if err != nil {
		nlog.Core().Debug("get per-user relay traffic failed", "error", err)
		return
	}
	s.tracker.ProcessRelayUser(relayUser)
}

func (s *Service) nextReportID() string {
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	seq := s.reportSeq.Add(1)
	if s.reportBoot == "" {
		s.reportBoot = newReportBootID()
	}
	return fmt.Sprintf("%s-%d", s.reportBoot, seq)
}

func (s *Service) rememberFailedReport(batch *reportBatch) {
	s.reportMu.Lock()
	s.retryReport = batch
	s.reportMu.Unlock()
}

func (s *Service) forgetCompletedReport(batch *reportBatch) {
	s.reportMu.Lock()
	if s.retryReport == batch {
		s.retryReport = nil
	}
	s.reportMu.Unlock()
}

func cloneTraffic(src map[int][2]int64) map[int][2]int64 {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[int][2]int64, len(src))
	for uid, value := range src {
		dst[uid] = value
	}
	return dst
}

func cloneRelayUserTraffic(src map[int]map[int][2]int64) map[int]map[int][2]int64 {
	if len(src) == 0 {
		return nil
	}
	out := make(map[int]map[int][2]int64, len(src))
	for uid, nodes := range src {
		if len(nodes) == 0 {
			continue
		}
		copyNodes := make(map[int][2]int64, len(nodes))
		for nodeID, value := range nodes {
			copyNodes[nodeID] = value
		}
		out[uid] = copyNodes
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneAliveIPs(src map[int][]string) map[int][]string {
	dst := make(map[int][]string, len(src))
	for uid, ips := range src {
		dst[uid] = append([]string(nil), ips...)
	}
	return dst
}

// buildMetrics aggregates node-level metrics to be reported to the panel.
// This includes active connections, per-core CPU, GC stats, API call stats,
// WebSocket status, and limiter hit counts.
func (s *Service) buildMetrics(status monitor.Status) map[string]interface{} {
	s.metricsMu.RLock()
	lastUsers := s.lastUsers
	wsClient := s.wsClient
	s.metricsMu.RUnlock()

	m := make(map[string]interface{})
	online := s.tracker.CurrentOnline()

	m["uptime"] = status.Uptime
	m["goroutines"] = status.Goroutines

	// Active connections (last measured during tracker.Process()).
	m["active_connections"] = s.tracker.ActiveConnections()
	m["total_connections"] = s.tracker.TotalConnections()
	m["active_users"] = len(online)
	m["total_users"] = len(lastUsers)

	// Speed
	m["inbound_speed"] = s.tracker.InboundSpeed()
	m["outbound_speed"] = s.tracker.OutboundSpeed()

	// Per-core CPU usage (if available).
	if len(status.CPUPerCore) > 0 {
		m["cpu_per_core"] = status.CPUPerCore
	}

	m["load"] = map[string]interface{}{
		"load1":  status.Load1,
		"load5":  status.Load5,
		"load15": status.Load15,
	}

	// Speed Limiter metrics
	m["speed_limiter"] = map[string]interface{}{
		"has_limits":    s.speedTracker.HasLimits(),
		"limited_users": s.speedTracker.LimitedUserCount(),
	}

	// GC metrics.
	m["gc"] = map[string]interface{}{
		"num_gc":        status.NumGC,
		"last_pause_ms": status.LastPauseMS,
	}

	// API metrics.
	api := s.source.Metrics()
	m["api"] = map[string]interface{}{
		"success": api.Success,
		"failure": api.Failure,
	}

	// WebSocket status.
	wsEnabled := wsClient != nil
	wsConnected := wsEnabled && wsClient.IsConnected()
	m["ws"] = map[string]interface{}{
		"enabled":   wsEnabled,
		"connected": wsConnected,
	}

	// Limiter metrics.
	lm := s.limiter.SnapshotMetrics()
	m["limits"] = map[string]interface{}{
		"device_limit_events": lm.DeviceLimitEvents,
		"conn_limit_events":   lm.ConnLimitEvents,
		"speed_limited_users": s.speedTracker.LimitedUserCount(),
	}

	return m
}

// applyDeviceIPExclude 更新不计入设备数的来源名单，只影响后续计数和上报，不重载内核。
func applyDeviceIPExclude(cfg *model.NodeSpec) {
	invalid, changed := deviceip.SetExcluded(cfg.DeviceIPExclude)
	if len(invalid) > 0 {
		nlog.Core().Warn("device ip exclude list has invalid entries, skipped", "entries", invalid)
	}
	if changed {
		nlog.Core().Info("device ip exclude list updated", "entries", len(cfg.DeviceIPExclude)-len(invalid))
	}
}

// computeConfigHash returns a deterministic hash of the node config.
// It uses JSON marshaling to ensure all fields are captured, ensuring that
// any configuration change correctly triggers a kernel reload.
func computeConfigHash(cfg *model.NodeSpec) string {
	if cfg == nil {
		return ""
	}
	h := sha256.New()
	// We marshal the entire config to be safe. Node config updates are low-frequency,
	// so the robustness of capturing all fields outweighs the micro-performance of manual hashing.
	data, _ := json.Marshal(cfg)
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// computeUserHash returns a deterministic hash of the user list for change detection.
// Uses direct byte encoding instead of binary.Write to avoid reflection overhead.
func computeUserHash(users []model.UserSpec) string {
	sorted := make([]model.UserSpec, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	h := sha256.New()
	var buf [8]byte
	for _, u := range sorted {
		binary.LittleEndian.PutUint64(buf[:], uint64(u.ID))
		h.Write(buf[:])
		io.WriteString(h, u.UUID)
		binary.LittleEndian.PutUint64(buf[:], uint64(u.SpeedLimit))
		h.Write(buf[:])
		binary.LittleEndian.PutUint64(buf[:], uint64(u.DeviceLimit))
		h.Write(buf[:])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ─── Device management ──────────────────────────────────────────────────

// sendDeviceBatch reports local device snapshot to panel via WS.
func (s *Service) sendDeviceBatch() {
	if s.wsClient == nil || !s.wsClient.IsConnected() {
		return
	}

	devices := s.tracker.FlushAliveIPs()
	s.sink.ReportDevices(s.wsClient, devices)
	nlog.Core().Debug("device snapshot sent", "users", len(devices))
}

// reportDevices periodically reports device snapshot to panel.
func (s *Service) reportDevices() {
	s.sendDeviceBatch()
}

// ─── Runtime validation ─────────────────────────────────────────────────

func validateNodeRuntime(cfg *config.Config, kcfgSupported []string, spec *model.NodeSpec, tls kernel.TLSCert) error {
	if spec == nil {
		return fmt.Errorf("node spec is nil")
	}
	if !containsString(kcfgSupported, spec.Protocol) {
		return fmt.Errorf("protocol %q is not supported by kernel %q", spec.Protocol, cfgKernelType(cfg))
	}
	var kcfg config.KernelConfig
	if cfg != nil {
		kcfg = cfg.Kernel
	}
	if err := model.ValidateNodeSpec(spec, kcfg); err != nil {
		return err
	}
	if err := validateTLSRequirements(spec, tls, cfgKernelType(cfg)); err != nil {
		return err
	}
	if err := validateRuntimeCertConfig(spec); err != nil {
		return err
	}
	return nil
}

func validateTLSRequirements(spec *model.NodeSpec, tls kernel.TLSCert, kernelType string) error {
	needsCert := false
	switch spec.Protocol {
	case "hysteria", "hysteria2", "tuic", "anytls":
		needsCert = true
	case "trojan":
		if spec.TLS != 2 {
			needsCert = true
		}
	}
	if needsCert && !hasUsableTLSConfig(spec, tls) {
		return fmt.Errorf("protocol %q requires TLS certificate files", spec.Protocol)
	}
	if spec.TLS == 2 {
		if err := validateRealityRequirements(spec, kernelType); err != nil {
			return err
		}
	}
	return nil
}

func hasUsableTLSConfig(spec *model.NodeSpec, tls kernel.TLSCert) bool {
	if tls.HasCert() {
		return true
	}
	if spec == nil || spec.CertConfig == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(spec.CertConfig.CertMode))
	switch mode {
	case "self":
		return true
	case "content":
		return strings.TrimSpace(spec.CertConfig.CertContent) != "" && strings.TrimSpace(spec.CertConfig.KeyContent) != ""
	case "file":
		return strings.TrimSpace(spec.CertConfig.CertFile) != "" && strings.TrimSpace(spec.CertConfig.KeyFile) != ""
	case "http":
		return strings.TrimSpace(spec.CertConfig.Domain) != ""
	case "dns":
		return strings.TrimSpace(spec.CertConfig.Domain) != "" && strings.TrimSpace(spec.CertConfig.DNSProvider) != ""
	default:
		return false
	}
}

func validateRuntimeCertConfig(spec *model.NodeSpec) error {
	if spec == nil || spec.CertConfig == nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(spec.CertConfig.CertMode))
	if mode != "dns" {
		return nil
	}
	provider := strings.TrimSpace(spec.CertConfig.DNSProvider)
	if provider == "" {
		return fmt.Errorf("dns cert mode requires cert_config.dns_provider")
	}
	if _, ok := dnsproviders.Get(provider); !ok {
		return fmt.Errorf("unsupported cert_config.dns_provider %q (supported: %s)", provider, strings.Join(dnsproviders.CanonicalNames(), ", "))
	}
	return nil
}

func validateRealityRequirements(spec *model.NodeSpec, _ string) error {
	if spec.TLSSettings == nil {
		return fmt.Errorf("reality tls requires tls_settings")
	}
	privateKey := strings.TrimSpace(stringValue(spec.TLSSettings["private_key"]))
	serverName := strings.TrimSpace(stringValue(spec.TLSSettings["server_name"]))
	dest := strings.TrimSpace(stringValue(spec.TLSSettings["dest"]))
	if privateKey == "" {
		return fmt.Errorf("reality tls requires tls_settings.private_key")
	}
	if serverName == "" && dest == "" {
		return fmt.Errorf("reality tls requires tls_settings.server_name or tls_settings.dest")
	}
	return nil
}

func cfgKernelType(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(cfg.Kernel.Type))
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	default:
		return ""
	}
}
