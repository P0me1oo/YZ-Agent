package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/config"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

type PanelControlPlane struct {
	cfg    config.PanelConfig
	wsCfg  config.WSConfig
	client *panel.Client
}

type panelPushClient struct {
	inner *panel.WSClient
}

func NewPanelControlPlane(panelCfg config.PanelConfig, wsCfg config.WSConfig) *PanelControlPlane {
	return &PanelControlPlane{
		cfg:    panelCfg,
		wsCfg:  wsCfg,
		client: panel.NewClient(panelCfg),
	}
}

func (p *PanelControlPlane) SupportsPolling() bool       { return true }
func (p *PanelControlPlane) SupportsDiscovery() bool     { return true }
func (p *PanelControlPlane) SupportsReporting() bool     { return true }
func (p *PanelControlPlane) SupportsDeviceReports() bool { return true }

func (p *PanelControlPlane) Initial(ctx context.Context, metricsFn func() map[string]interface{}, events chan<- Event, statuses chan<- StatusChange) (Bootstrap, error) {
	hs, err := p.client.Handshake()
	if err != nil {
		return Bootstrap{}, fmt.Errorf("handshake: %w", err)
	}

	bootstrap := Bootstrap{PushInterval: hs.Settings.PushInterval, PullInterval: hs.Settings.PullInterval}
	if hs.WebSocket.Enabled && hs.WebSocket.WSURL != "" {
		bootstrap.Push = p.newPushClient(metricsFn, events, statuses, hs.WebSocket.WSURL)
		return bootstrap, nil
	}

	nlog.Core().Info("websocket disabled, using REST API")
	configETag, userETag := p.client.ETags()
	committed := false
	defer func() {
		if !committed {
			p.client.RestoreETags(configETag, userETag)
		}
	}()
	configSnapshot, err := p.client.GetConfig()
	if err != nil {
		return Bootstrap{}, fmt.Errorf("initial config fetch: %w", err)
	}
	if configSnapshot == nil {
		return Bootstrap{}, fmt.Errorf("initial config is nil")
	}
	users, err := p.client.GetUsers()
	if err != nil {
		return Bootstrap{}, fmt.Errorf("initial user fetch: %w", err)
	}
	// 配置校验由节点服务负责；无效快照也必须送达，才能停止旧内核并等待修正。
	bootstrap.Config = model.NodeSpecFromPanel(configSnapshot)
	bootstrap.Users = model.UserSpecsFromPanel(users)
	committed = true
	return bootstrap, nil
}

func (p *PanelControlPlane) Poll(ctx context.Context) (Snapshot, error) {
	configETag, userETag := p.client.ETags()
	committed := false
	defer func() {
		if !committed {
			p.client.RestoreETags(configETag, userETag)
		}
	}()

	configSnapshot, err := p.client.GetConfig()
	if err != nil {
		return Snapshot{}, fmt.Errorf("poll config: %w", err)
	}
	users, err := p.client.GetUsers()
	if err != nil {
		return Snapshot{}, fmt.Errorf("poll users: %w", err)
	}
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	default:
	}
	committed = true
	return Snapshot{Config: model.NodeSpecFromPanel(configSnapshot), Users: model.UserSpecsFromPanel(users)}, nil
}

func (p *PanelControlPlane) ResetPollingState() {
	p.client.ResetETags()
}

func (p *PanelControlPlane) Discover(ctx context.Context, metricsFn func() map[string]interface{}, events chan<- Event, statuses chan<- StatusChange) (PushClient, error) {
	hs, err := p.client.Handshake()
	if err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if hs.WebSocket.Enabled && hs.WebSocket.WSURL != "" {
		return p.newPushClient(metricsFn, events, statuses, hs.WebSocket.WSURL), nil
	}
	return nil, nil
}

func (p *PanelControlPlane) Report(payload ReportPayload) error {
	return p.ReportContext(context.Background(), payload)
}

func (p *PanelControlPlane) ReportContext(ctx context.Context, payload ReportPayload) error {
	return p.client.ReportContext(ctx, payload.ReportID, payload.Traffic, payload.RelayTraffic, payload.RelayUserTraffic, payload.Alive, payload.Online, payload.CPU, payload.Mem, payload.Swap, payload.Disk, payload.Metrics, payload.LimitEvents)
}

func (p *PanelControlPlane) ReportDevices(push PushClient, devices map[int][]string) {
	if push != nil {
		push.SendDeviceReport(devices)
	}
}

func (p *PanelControlPlane) Metrics() APIMetrics {
	api := p.client.SnapshotMetrics()
	return APIMetrics{Success: api.Success, Failure: api.Failure}
}

func (p *PanelControlPlane) newPushClient(metricsFn func() map[string]interface{}, events chan<- Event, statuses chan<- StatusChange, wsURL string) PushClient {
	cfg := panel.WSClientConfig{
		StatusInterval:   time.Duration(p.wsCfg.StatusInterval) * time.Second,
		HandshakeTimeout: time.Duration(p.wsCfg.HandshakeTimeout) * time.Second,
		BackoffInitial:   time.Duration(p.wsCfg.BackoffInitial) * time.Second,
		BackoffMax:       time.Duration(p.wsCfg.BackoffMax) * time.Second,
	}
	inner := panel.NewWSClient(
		wsURL,
		p.cfg.Token,
		p.cfg.NodeID,
		cfg,
		func(event panel.WSEvent) {
			translated := TranslateWSEvent(event)
			select {
			case events <- translated:
			default:
				nlog.Core().Warn("ws event channel full, dropping event", "type", translated.Type)
				select {
				case statuses <- StatusChange{Connected: true, NeedsResync: true}:
				default:
				}
			}
		},
		func(status panel.WSStatusChange) {
			select {
			case statuses <- StatusChange{Connected: status.Connected}:
			default:
			}
		},
		metricsFn,
	)
	return &panelPushClient{inner: inner}
}

// TranslateWSEvent 保留待应用快照，由目标节点按自身内核校验并报告失败。
func TranslateWSEvent(event panel.WSEvent) Event {
	translated := Event{Type: EventType(event.Type), DeltaAction: event.DeltaAction, DeviceUsers: event.DeviceUsers}
	if event.Config != nil {
		translated.Config = model.NodeSpecFromPanel(event.Config)
	}
	if event.Users != nil {
		translated.Users = model.UserSpecsFromPanel(event.Users)
	}
	if event.DeltaUsers != nil {
		translated.DeltaUsers = model.UserSpecsFromPanel(event.DeltaUsers)
	}
	return translated
}

func (p *panelPushClient) Run(ctx context.Context) { p.inner.Run(ctx) }
func (p *panelPushClient) IsConnected() bool       { return p.inner.IsConnected() }
func (p *panelPushClient) SendDeviceReport(devices map[int][]string) {
	p.inner.SendDeviceReport(devices)
}
