package controlplane

import (
	"context"
	"fmt"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

// MachinePanelControlPlane implements ControlPlane for a single node running
// under the machine orchestrator. REST calls go through a per-node panel.Client
// with machine-level auth; WS events are delivered via an externally-managed
// PushClient supplied by the machine WS mux.
type MachinePanelControlPlane struct {
	client *panel.Client
	push   PushClient // virtual push client from WS mux (may be nil)

	registerFn func(statuses chan<- StatusChange) *NodeMailbox
}

// NewMachinePanelControlPlane creates a per-node control plane for machine mode.
// push may be nil if WS is unavailable.
// registerFn is called during Initial() to wire machine mailbox access and status
// updates into the service loop. It may be nil when WS is not used.
func NewMachinePanelControlPlane(
	client *panel.Client,
	push PushClient,
	registerFn func(statuses chan<- StatusChange) *NodeMailbox,
) *MachinePanelControlPlane {
	return &MachinePanelControlPlane{
		client:     client,
		push:       push,
		registerFn: registerFn,
	}
}

func (p *MachinePanelControlPlane) SupportsPolling() bool       { return true }
func (p *MachinePanelControlPlane) SupportsDiscovery() bool     { return false }
func (p *MachinePanelControlPlane) SupportsReporting() bool     { return true }
func (p *MachinePanelControlPlane) SupportsDeviceReports() bool { return p.push != nil }

func (p *MachinePanelControlPlane) Initial(
	ctx context.Context,
	metricsFn func() map[string]interface{},
	events chan<- Event,
	statuses chan<- StatusChange,
) (Bootstrap, error) {
	// In machine mode the orchestrator handles the shared WS handshake, but each
	// node service still needs one initial REST snapshot so newly discovered nodes
	// can start immediately instead of waiting for a subsequent WS full-sync.
	hs, err := p.client.Handshake()
	if err != nil {
		nlog.Core().Debug("machine node handshake skipped, using REST", "error", err)
	}

	bootstrap := Bootstrap{}
	if hs != nil {
		bootstrap.PushInterval = hs.Settings.PushInterval
		bootstrap.PullInterval = hs.Settings.PullInterval
	}

	configETag, userETag := p.client.ETags()
	committed := false
	defer func() {
		if !committed {
			p.client.RestoreETags(configETag, userETag)
		}
	}()

	configSnapshot, err := p.client.GetConfig()
	if err != nil {
		return Bootstrap{}, fmt.Errorf("machine initial config: %w", err)
	}
	if configSnapshot == nil {
		return Bootstrap{}, fmt.Errorf("machine initial config is nil")
	}
	users, err := p.client.GetUsers()
	if err != nil {
		return Bootstrap{}, fmt.Errorf("machine initial users: %w", err)
	}
	bootstrap.Config = model.NodeSpecFromPanel(configSnapshot)
	bootstrap.Users = model.UserSpecsFromPanel(users)
	committed = true

	// Register mailbox access after the initial snapshot is prepared, so the
	// Service can start the kernel immediately and only then drain coalesced WS
	// state from the machine mailbox.
	if p.push != nil {
		if p.registerFn != nil {
			bootstrap.Mailbox = p.registerFn(statuses)
		}
		bootstrap.Push = p.push
	}

	return bootstrap, nil
}

func (p *MachinePanelControlPlane) Poll(ctx context.Context) (Snapshot, error) {
	configETag, userETag := p.client.ETags()
	committed := false
	defer func() {
		if !committed {
			p.client.RestoreETags(configETag, userETag)
		}
	}()

	configSnapshot, err := p.client.GetConfig()
	if err != nil {
		return Snapshot{}, fmt.Errorf("machine poll config: %w", err)
	}
	users, err := p.client.GetUsers()
	if err != nil {
		return Snapshot{}, fmt.Errorf("machine poll users: %w", err)
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	default:
	}
	committed = true
	return Snapshot{Config: model.NodeSpecFromPanel(configSnapshot), Users: model.UserSpecsFromPanel(users)}, nil
}

func (p *MachinePanelControlPlane) ResetPollingState() {
	p.client.ResetETags()
}

func (p *MachinePanelControlPlane) Discover(
	ctx context.Context,
	metricsFn func() map[string]interface{},
	events chan<- Event,
	statuses chan<- StatusChange,
) (PushClient, error) {
	return nil, nil
}

func (p *MachinePanelControlPlane) Report(payload ReportPayload) error {
	return p.ReportContext(context.Background(), payload)
}

func (p *MachinePanelControlPlane) ReportContext(ctx context.Context, payload ReportPayload) error {
	return p.client.ReportContext(ctx,
		payload.ReportID,
		payload.Traffic, payload.RelayTraffic, payload.RelayUserTraffic, payload.Alive, payload.Online,
		payload.CPU, payload.Mem, payload.Swap, payload.Disk,
		payload.Metrics,
		payload.LimitEvents,
	)
}

func (p *MachinePanelControlPlane) ReportDevices(push PushClient, devices map[int][]string) {
	if push != nil {
		push.SendDeviceReport(devices)
	}
}

func (p *MachinePanelControlPlane) Metrics() APIMetrics {
	api := p.client.SnapshotMetrics()
	return APIMetrics{Success: api.Success, Failure: api.Failure}
}
