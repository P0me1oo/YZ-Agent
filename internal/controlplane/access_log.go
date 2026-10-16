package controlplane

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
)

type AccessLogSink interface {
	AccessLogEnabled() bool
	ReportAccessLog(context.Context, *accesslog.Batch) error
}

func (p *PanelControlPlane) AccessLogEnabled() bool { return p.client.AccessLogEnabled() }
func (p *PanelControlPlane) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportAccessLog(ctx, batch)
}
func (p *MachinePanelControlPlane) AccessLogEnabled() bool { return p.client.AccessLogEnabled() }
func (p *MachinePanelControlPlane) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	return p.client.ReportAccessLog(ctx, batch)
}
