package controlplane

import (
	"context"

	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/P0me1oo/YZ-Agent/internal/panel"
)

func pollRealtime(ctx context.Context, client *panel.Client) (Snapshot, error) {
	state, err := client.GetControlState(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	devices := make(map[int][]string, len(state.Devices.Users))
	for userID, ips := range state.Devices.Users {
		devices[userID] = []string(ips)
	}
	return Snapshot{
		Config:         model.NodeSpecFromPanel(&state.Control.Config),
		Users:          model.UserSpecsFromPanel(state.Control.Users),
		ControlVersion: state.Control.StateVersion,
		DeviceVersion:  state.Devices.StateVersion,
		DeviceUsers:    devices,
	}, nil
}

func (p *PanelControlPlane) RealtimeEnabled() bool { return p.client.RealtimeEnabled() }
func (p *PanelControlPlane) PublishState(ctx context.Context, state panel.StatePayload) error {
	return p.client.PublishState(ctx, state)
}
func (p *MachinePanelControlPlane) RealtimeEnabled() bool { return p.client.RealtimeEnabled() }
func (p *MachinePanelControlPlane) PublishState(ctx context.Context, state panel.StatePayload) error {
	return p.client.PublishState(ctx, state)
}
func (p *panelPushClient) WebSocket() *panel.WSClient { return p.inner }

func (p *PanelControlPlane) TelemetryNeeds() (bool, bool)        { return p.client.TelemetryNeeds() }
func (p *MachinePanelControlPlane) TelemetryNeeds() (bool, bool) { return p.client.TelemetryNeeds() }
