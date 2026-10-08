package controlplane

import "github.com/P0me1oo/YZ-Agent/internal/devicegate"

func (p *PanelControlPlane) DeviceHandoverClient() devicegate.Remote        { return p.client }
func (p *MachinePanelControlPlane) DeviceHandoverClient() devicegate.Remote { return p.client }
