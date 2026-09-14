// Package machine implements the machine-mode orchestrator that dynamically
// discovers nodes from the panel's machine API and manages their lifecycles.
package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/controlplane"
	"github.com/P0me1oo/YZ-Agent/internal/firewall"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/monitor"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/P0me1oo/YZ-Agent/internal/service"
)

// nodeHandle tracks a running node service.
type nodeHandle struct {
	cancel           context.CancelFunc
	done             chan struct{}
	mailbox          *controlplane.NodeMailbox
	kernel           string // actual runtime kernel after transport compatibility resolution
	configuredKernel string // panel-selected kernel used to detect selection changes
}

// Orchestrator manages all nodes bound to a panel machine. It:
//   - discovers nodes via GET /machine/nodes
//   - starts / stops Service instances as nodes are added / removed
//   - maintains a shared WS connection that demuxes events by node_id
//   - reports machine-level load via POST /machine/status
type Orchestrator struct {
	cfg      *config.Config
	firewall firewall.Controller
	client   *panel.Client // machine-level client (no node_id)

	mu    sync.Mutex
	nodes map[int]*nodeHandle // node_id → handle

	// Per-node mailbox keyed by node_id. Shared WS events are aggregated here
	// and each node service drains the latest state when ready.
	eventsMu  sync.RWMutex
	mailboxes map[int]*controlplane.NodeMailbox
	statuses  map[int]chan<- controlplane.StatusChange

	// Shared WS client (nil when WS is disabled).
	ws       *panel.WSClient
	wsCancel context.CancelFunc

	// runCtx is stored from Run() so that onWSEvent can trigger rediscover
	// for sync.nodes events without blocking the main loop.
	runCtx context.Context

	pullInterval time.Duration
	pushInterval time.Duration

	statusMu      sync.Mutex
	nodeStatuses  map[int]service.RuntimeStatus
	statusHandler func(service.RuntimeStatus)
}

// New creates a machine orchestrator from the given config.
func New(cfg *config.Config) *Orchestrator {
	panelCfg := config.PanelConfig{
		URL:       cfg.Panel.URL,
		Token:     cfg.Machine.Token,
		MachineID: cfg.Machine.MachineID,
	}
	return &Orchestrator{
		cfg:          cfg,
		client:       panel.NewClient(panelCfg),
		nodes:        make(map[int]*nodeHandle),
		mailboxes:    make(map[int]*controlplane.NodeMailbox),
		statuses:     make(map[int]chan<- controlplane.StatusChange),
		nodeStatuses: make(map[int]service.RuntimeStatus),
	}
}

// SetStatusHandler reports the aggregate state of all managed nodes.
func (o *Orchestrator) SetStatusHandler(handler func(service.RuntimeStatus)) {
	o.statusHandler = handler
}

func (o *Orchestrator) SetFirewallController(controller firewall.Controller) { o.firewall = controller }

func (o *Orchestrator) notifyStatus(status service.RuntimeStatus) {
	if o.statusHandler != nil {
		o.statusHandler(status)
	}
}

func (o *Orchestrator) setNodeStatus(nodeID int, status service.RuntimeStatus) {
	o.statusMu.Lock()
	o.nodeStatuses[nodeID] = status
	aggregate := o.aggregateNodeStatusLocked()
	o.statusMu.Unlock()
	o.notifyStatus(aggregate)
}

func (o *Orchestrator) removeNodeStatus(nodeID int) {
	o.statusMu.Lock()
	delete(o.nodeStatuses, nodeID)
	aggregate := o.aggregateNodeStatusLocked()
	o.statusMu.Unlock()
	o.notifyStatus(aggregate)
}

func (o *Orchestrator) reconcileNodeStatuses(wanted map[int]panel.MachineNode) {
	o.statusMu.Lock()
	for nodeID := range o.nodeStatuses {
		if _, ok := wanted[nodeID]; !ok {
			delete(o.nodeStatuses, nodeID)
		}
	}
	for nodeID := range wanted {
		if _, ok := o.nodeStatuses[nodeID]; !ok {
			o.nodeStatuses[nodeID] = service.RuntimeStarting
		}
	}
	aggregate := o.aggregateNodeStatusLocked()
	o.statusMu.Unlock()
	o.notifyStatus(aggregate)
}

func (o *Orchestrator) aggregateNodeStatusLocked() service.RuntimeStatus {
	aggregate := service.RuntimeRunning
	for _, nodeStatus := range o.nodeStatuses {
		if nodeStatus == service.RuntimeFailed {
			return service.RuntimeFailed
		}
		if nodeStatus == service.RuntimeStarting || nodeStatus == service.RuntimeStopped {
			aggregate = service.RuntimeStarting
		}
	}
	return aggregate
}

// Run is the main loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.notifyStatus(service.RuntimeStarting)
	o.runCtx = ctx
	nodesResp, err := o.client.GetMachineNodes()
	if err != nil {
		o.notifyStatus(service.RuntimeFailed)
		return fmt.Errorf("initial node discovery: %w", err)
	}

	o.applyIntervals(nodesResp.BaseConfig)
	nlog.Core().Info(fmt.Sprintf("machine %d: discovered %d nodes",
		o.cfg.Machine.MachineID, len(nodesResp.Nodes)))

	wanted := make(map[int]panel.MachineNode, len(nodesResp.Nodes))
	for _, node := range nodesResp.Nodes {
		wanted[node.ID] = node
	}
	o.reconcileNodeStatuses(wanted)

	// Start machine-level WS as early as possible so sync.nodes can reach an
	// empty machine before the first node is attached.
	o.tryStartWS(ctx)

	// Start initial nodes.
	for _, n := range nodesResp.Nodes {
		o.startNode(ctx, n)
	}
	discoveryTicker := time.NewTicker(o.pullInterval)
	statusTicker := time.NewTicker(o.pushInterval)
	defer discoveryTicker.Stop()
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			o.stopAll()
			o.notifyStatus(service.RuntimeStopped)
			return nil

		case <-discoveryTicker.C:
			o.rediscover(ctx)

		case <-statusTicker.C:
			o.reportMachineStatus()
		}
	}
}

// ─── Node lifecycle ──────────────────────────────────────────────────────

func (o *Orchestrator) startNode(ctx context.Context, mn panel.MachineNode) {
	wantedKernel := o.machineNodeKernel(mn)
	o.mu.Lock()
	if current, exists := o.nodes[mn.ID]; exists {
		if current.configuredKernel == wantedKernel {
			o.mu.Unlock()
			return
		}
		o.mu.Unlock()
		// Kernel changes cannot be hot-reloaded because the kernel backend is
		// selected when the Service is constructed. Restart only this node.
		o.stopNode(mn.ID)
		o.mu.Lock()
		if _, exists := o.nodes[mn.ID]; exists {
			o.mu.Unlock()
			return
		}
	}

	nodeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	mb := controlplane.NewNodeMailbox()
	o.nodes[mn.ID] = &nodeHandle{
		cancel: cancel, done: done, mailbox: mb,
		kernel: wantedKernel, configuredKernel: wantedKernel,
	}
	o.mu.Unlock()

	o.eventsMu.Lock()
	o.mailboxes[mn.ID] = mb
	o.eventsMu.Unlock()
	o.setNodeStatus(mn.ID, service.RuntimeStarting)

	nodeCfg := o.cfg.ExpandMachineNode(mn.ID, mn.Type)
	nodeCfg.Kernel.Type = wantedKernel

	perNodeClient := o.client.ForNode(mn.ID)

	// Pre-fetch node config to detect transport-based kernel requirements.
	// If the transport (e.g. xhttp) is incompatible with the configured kernel
	// (e.g. singbox), auto-switch to the required kernel for this node.
	if cfgSnapshot, err := perNodeClient.GetConfig(); err == nil && cfgSnapshot != nil {
		if snapshotKernel := strings.TrimSpace(cfgSnapshot.KernelType); snapshotKernel != "" {
			if normalized, normalizeErr := model.NormalizeKernelType(snapshotKernel); normalizeErr == nil {
				nodeCfg.Kernel.Type = normalized
			}
		}
		if resolved := model.ResolveKernelForTransport(cfgSnapshot.Network, nodeCfg.Kernel.Type); resolved != nodeCfg.Kernel.Type {
			nlog.Core().Info(fmt.Sprintf("machine: auto-switching kernel for node %d (%s→%s, transport=%s)",
				mn.ID, nodeCfg.Kernel.Type, resolved, cfgSnapshot.Network))
			nodeCfg.Kernel.Type = resolved
		}
	}
	o.mu.Lock()
	if handle, ok := o.nodes[mn.ID]; ok {
		handle.kernel = nodeCfg.Kernel.Type
	}
	o.mu.Unlock()
	// Reset cached ETag so the subsequent GetConfig in Initial() gets a full response.
	perNodeClient.ResetConfigETag()

	var push controlplane.PushClient
	if o.ws != nil {
		push = &machineNodePush{
			nodeID: mn.ID,
			ws:     o.ws,
		}
	}

	// The registerFn is called by MachinePanelControlPlane.Initial() to expose
	// the node mailbox + status channel to the Service.
	nodeID := mn.ID
	registerFn := func(st chan<- controlplane.StatusChange) *controlplane.NodeMailbox {
		o.registerNode(nodeID, st)
		return mb
	}

	cp := controlplane.NewMachinePanelControlPlane(perNodeClient, push, registerFn)
	svc := service.NewWithControlPlane(nodeCfg, cp)
	svc.SetFirewallController(o.firewall)
	svc.SetStatusHandler(func(status service.RuntimeStatus) {
		o.setNodeStatus(mn.ID, status)
	})

	nlog.Core().Info(fmt.Sprintf("machine: starting node %d (%s/%s)",
		mn.ID, mn.Type, mn.Name))

	go func() {
		defer close(done)
		err := svc.Run(nodeCtx)
		if err != nil {
			nlog.Core().Error("machine node exited with error",
				"node_id", mn.ID, "error", err)
		}
		o.unregisterNode(mn.ID)
		o.finishNode(mn.ID, done, err)
	}()
}

func (o *Orchestrator) machineNodeKernel(mn panel.MachineNode) string {
	if normalized, err := model.NormalizeKernelType(mn.KernelType); err == nil {
		return normalized
	}
	if normalized, err := model.NormalizeKernelType(o.cfg.Kernel.Type); err == nil {
		return normalized
	}
	return "xray"
}

func (o *Orchestrator) finishNode(nodeID int, done chan struct{}, runErr error) {
	o.mu.Lock()
	if current, ok := o.nodes[nodeID]; ok && current.done == done {
		delete(o.nodes, nodeID)
	}
	o.mu.Unlock()
	if runErr != nil {
		o.setNodeStatus(nodeID, service.RuntimeFailed)
	}
}

func (o *Orchestrator) stopNode(nodeID int) {
	o.mu.Lock()
	h, ok := o.nodes[nodeID]
	if !ok {
		o.mu.Unlock()
		return
	}
	o.mu.Unlock()

	o.eventsMu.Lock()
	delete(o.mailboxes, nodeID)
	o.eventsMu.Unlock()

	nlog.Core().Info(fmt.Sprintf("machine: stopping node %d", nodeID))
	h.cancel()
	<-h.done
	o.mu.Lock()
	if current, ok := o.nodes[nodeID]; ok && current == h {
		delete(o.nodes, nodeID)
	}
	o.mu.Unlock()
	o.removeNodeStatus(nodeID)
}

func (o *Orchestrator) stopAll() {
	o.mu.Lock()
	handles := make(map[int]*nodeHandle, len(o.nodes))
	for id, h := range o.nodes {
		handles[id] = h
	}
	o.mu.Unlock()

	for id, h := range handles {
		nlog.Core().Info(fmt.Sprintf("machine: stopping node %d", id))
		h.cancel()
	}
	for _, h := range handles {
		<-h.done
	}

	if o.wsCancel != nil {
		o.wsCancel()
	}
}

// ─── Node discovery ──────────────────────────────────────────────────────

func (o *Orchestrator) rediscover(ctx context.Context) {
	nodesResp, err := o.client.GetMachineNodes()
	if err != nil {
		nlog.Core().Warn("machine node discovery failed", "error", err)
		return
	}

	wanted := make(map[int]panel.MachineNode, len(nodesResp.Nodes))
	for _, n := range nodesResp.Nodes {
		wanted[n.ID] = n
	}

	o.mu.Lock()
	var toRemove []int
	for id := range o.nodes {
		if _, ok := wanted[id]; !ok {
			toRemove = append(toRemove, id)
		}
	}
	o.mu.Unlock()

	for _, id := range toRemove {
		o.stopNode(id)
	}
	o.reconcileNodeStatuses(wanted)

	for _, n := range nodesResp.Nodes {
		o.startNode(ctx, n) // no-op if already running
	}
}

// ─── Machine status reporting ────────────────────────────────────────────

func (o *Orchestrator) reportMachineStatus() {
	s := monitor.Collect()
	if err := o.client.ReportMachineStatus(
		s.CPU,
		[2]uint64{s.MemTotal, s.MemUsed},
		[2]uint64{s.SwapTotal, s.SwapUsed},
		[2]uint64{s.DiskTotal, s.DiskUsed},
		s.NetInSpeed, s.NetOutSpeed,
	); err != nil {
		nlog.Core().Warn("machine status report failed", "error", err)
	}
}

// ─── WS mux ─────────────────────────────────────────────────────────────

func (o *Orchestrator) tryStartWS(ctx context.Context) {
	hs, err := o.client.Handshake()
	if err != nil {
		nlog.Core().Warn("machine ws handshake failed, REST only", "error", err)
		return
	}
	if !hs.WebSocket.Enabled || hs.WebSocket.WSURL == "" {
		nlog.Core().Info("machine: ws disabled by panel, REST only")
		return
	}

	wsCfg := panel.WSClientConfig{
		StatusInterval:   time.Duration(o.cfg.WS.StatusInterval) * time.Second,
		HandshakeTimeout: time.Duration(o.cfg.WS.HandshakeTimeout) * time.Second,
		BackoffInitial:   time.Duration(o.cfg.WS.BackoffInitial) * time.Second,
		BackoffMax:       time.Duration(o.cfg.WS.BackoffMax) * time.Second,
		MachineID:        o.cfg.Machine.MachineID,
	}

	o.ws = panel.NewWSClient(
		hs.WebSocket.WSURL,
		o.cfg.Machine.Token,
		0, // no single node_id
		wsCfg,
		o.onWSEvent,
		o.onWSStatus,
		nil, // per-node status is sent via machineNodePush
	)

	wsCtx, wsCancel := context.WithCancel(ctx)
	o.wsCancel = wsCancel
	go o.ws.Run(wsCtx)

	nlog.Core().Info("machine: ws mux started")
}

// onWSEvent routes a WS event to the correct node's channel.
// sync.nodes is a machine-level event that triggers immediate rediscovery.
func (o *Orchestrator) onWSEvent(event panel.WSEvent) {
	// sync.nodes is a machine-level event, not per-node
	if event.Type == panel.WSEventSyncNodes {
		nlog.Core().Info("machine received sync.nodes, triggering immediate rediscovery")
		go o.rediscover(o.runCtx)
		return
	}

	nodeID := event.NodeID
	if nodeID == 0 {
		nlog.Core().Debug("machine ws event missing node_id, dropping", "type", event.Type)
		return
	}

	// 无效配置同样送到对应节点，由该节点停止内核并保留待修正状态。
	translated := controlplane.TranslateWSEvent(event)

	o.eventsMu.RLock()
	mailbox, ok := o.mailboxes[nodeID]
	o.eventsMu.RUnlock()
	if !ok {
		nlog.Core().Debug("machine ws event for unknown node", "node_id", nodeID, "type", event.Type)
		return
	}
	mailbox.Apply(translated)
}

// onWSStatus broadcasts WS connectivity changes to all registered nodes.
func (o *Orchestrator) onWSStatus(status panel.WSStatusChange) {
	change := controlplane.StatusChange{Connected: status.Connected}
	o.eventsMu.RLock()
	defer o.eventsMu.RUnlock()
	for _, ch := range o.statuses {
		select {
		case ch <- change:
		default:
		}
	}
}

func (o *Orchestrator) registerNode(nodeID int, st chan<- controlplane.StatusChange) {
	o.eventsMu.Lock()
	o.statuses[nodeID] = st
	o.eventsMu.Unlock()
}

func (o *Orchestrator) unregisterNode(nodeID int) {
	o.eventsMu.Lock()
	delete(o.mailboxes, nodeID)
	delete(o.statuses, nodeID)
	o.eventsMu.Unlock()
}

func (o *Orchestrator) applyIntervals(bc panel.MachineBaseConfig) {
	o.pullInterval = time.Duration(bc.PullInterval) * time.Second
	if o.pullInterval < 30*time.Second {
		o.pullInterval = 60 * time.Second
	}
	o.pushInterval = time.Duration(bc.PushInterval) * time.Second
	if o.pushInterval < 10*time.Second {
		o.pushInterval = 60 * time.Second
	}
}

// ─── Virtual PushClient ─────────────────────────────────────────────────

// machineNodePush implements controlplane.PushClient for a single node
// backed by the shared machine WS connection. Events are routed by the
// WS mux directly to the Service's channels; this adapter only provides
// connectivity status and send capabilities.
type machineNodePush struct {
	nodeID int
	ws     *panel.WSClient
}

func (p *machineNodePush) Run(ctx context.Context) {
	// The shared WS mux pushes events into our channels; we just wait.
	<-ctx.Done()
}

func (p *machineNodePush) IsConnected() bool {
	return p.ws != nil && p.ws.IsConnected()
}

func (p *machineNodePush) SendDeviceReport(devices map[int][]string) {
	if p.ws == nil {
		return
	}
	payload := map[string]interface{}{
		"node_id": p.nodeID,
	}
	// Flatten into the standard format with node_id wrapper.
	strDevices := make(map[string][]string, len(devices))
	for uid, ips := range devices {
		strDevices[fmt.Sprintf("%d", uid)] = ips
	}
	payload["devices"] = strDevices
	data, _ := json.Marshal(payload)
	p.ws.SendRaw(panel.WSEventReportDevices, data)
}
