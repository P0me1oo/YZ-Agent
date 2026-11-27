package controlplane

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
)

type HealthReporter interface {
	ReportHealth(context.Context, *healthcheck.Result) error
}

func (p *PanelControlPlane) ReportHealth(ctx context.Context, r *healthcheck.Result) error {
	return p.client.ReportHealth(ctx, r)
}
func (p *MachinePanelControlPlane) ReportHealth(ctx context.Context, r *healthcheck.Result) error {
	return p.client.ReportHealth(ctx, r)
}
