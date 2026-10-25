package balancerkernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"

	"github.com/P0me1oo/YZ-Agent/internal/balancer"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"golang.org/x/time/rate"
)

// Gateway 是独立的四层入口，不解析或重复扣除用户流量。
type Gateway struct {
	mu      sync.Mutex
	runtime *balancer.Runtime
	config  *model.NodeSpec
}

func New() *Gateway                                { return &Gateway{} }
func (*Gateway) Name() string                      { return "balancer" }
func (*Gateway) Protocols() []string               { return []string{"balancer"} }
func (*Gateway) Capabilities() kernel.Capabilities { return kernel.Capabilities{} }
func (g *Gateway) Start(node *model.NodeSpec, _ []model.UserSpec, _ kernel.TLSCert) error {
	if node == nil || node.Balancer == nil || node.Protocol != "balancer" {
		return errors.New("调度入口配置缺失")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runtime != nil {
		g.runtime.Close()
		g.runtime = nil
	}
	runtime, err := balancer.Start(net.JoinHostPort(node.ListenIP, fmt.Sprint(node.ServerPort)), *node.Balancer)
	if err != nil {
		return err
	}
	g.runtime = runtime
	g.config = clone(node)
	return nil
}
func clone(node *model.NodeSpec) *model.NodeSpec {
	result := *node
	result.Balancer = balancer.CloneConfig(node.Balancer)
	return &result
}
func (g *Gateway) Reload(node *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	g.mu.Lock()
	if g.runtime != nil && node != nil && node.Balancer != nil {
		previous, next := clone(g.config), clone(node)
		previous.Balancer.Targets, next.Balancer.Targets = nil, nil
		if reflect.DeepEqual(previous, next) {
			err := g.runtime.UpdateTargets(node.Balancer.Targets)
			if err == nil {
				g.config = clone(node)
			}
			g.mu.Unlock()
			return err
		}
	}
	g.mu.Unlock()
	return g.Start(node, users, tls)
}
func (g *Gateway) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.runtime != nil {
		g.runtime.Close()
		g.runtime = nil
	}
}
func (g *Gateway) StopNow() { g.Stop() }
func (g *Gateway) IsRunning() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.runtime != nil && g.runtime.Running()
}
func (*Gateway) AddUsers([]model.UserSpec) (int, error)         { return 0, nil }
func (*Gateway) RemoveUsers([]model.UserSpec) (int, error)      { return 0, nil }
func (*Gateway) UpdateUsers([]model.UserSpec) (int, int, error) { return 0, 0, nil }
func (*Gateway) GetUserTraffic(context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	return nil, nil, 0, nil
}
func (*Gateway) CloseConnection(context.Context, string) error      { return nil }
func (*Gateway) CloseUserConnections(context.Context, string) error { return nil }
func (*Gateway) SetSpeedLimitFunc(func(string) *rate.Limiter)       {}
func (*Gateway) SetDeviceLimitFunc(func(string) (int, bool))        {}
func (*Gateway) SetConnLimiter(model.ConnLimiter)                   {}
func (*Gateway) UpdateGlobalDevices(map[int][]string)               {}
func (*Gateway) ClearGlobalDevices()                                {}

var _ kernel.Kernel = (*Gateway)(nil)
