package controlplane

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
)

type BlockLogSink interface {
	BlockLogEnabled() bool
	ReportBlockLog(context.Context, *accesslog.Batch) error
}

func (p *PanelControlPlane) BlockLogEnabled() bool { return p.client.BlockLogEnabled() }
func (p *PanelControlPlane) ReportBlockLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportBlockLog(ctx, batch)
}
func (p *MachinePanelControlPlane) BlockLogEnabled() bool { return p.client.BlockLogEnabled() }
func (p *MachinePanelControlPlane) ReportBlockLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportBlockLog(ctx, batch)
}
