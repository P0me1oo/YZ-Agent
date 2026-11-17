package controlplane

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
)

type AccessLogSink interface {
	AccessLogEnabled() bool
	ReportAccessLog(context.Context, *accesslog.Batch) error
}

func (p *PanelControlPlane) AccessLogEnabled() bool         { return p.client.AccessLogEnabled() }
func (p *PanelControlPlane) AccessLogState() (bool, uint64) { return p.client.AccessLogState() }
func (p *PanelControlPlane) AccessLogGeneration() string    { return p.client.AccessLogGeneration() }
func (p *PanelControlPlane) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportAccessLog(ctx, batch)
}
func (p *MachinePanelControlPlane) AccessLogEnabled() bool         { return p.client.AccessLogEnabled() }
func (p *MachinePanelControlPlane) AccessLogState() (bool, uint64) { return p.client.AccessLogState() }
func (p *MachinePanelControlPlane) AccessLogGeneration() string {
	return p.client.AccessLogGeneration()
}
func (p *MachinePanelControlPlane) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportAccessLog(ctx, batch)
}
