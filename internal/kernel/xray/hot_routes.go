package xray

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/kernel"
	"github.com/P0me1oo/YZ-Agent/internal/model"
	"github.com/xtls/xray-core/app/router"
	"github.com/xtls/xray-core/features/routing"
	"github.com/xtls/xray-core/infra/conf/serial"
	"reflect"
)

// tryHotRoutes 只替换现有出站之间的路由规则；监听、TLS、出站及路由静态字段变化仍重建实例。
func (x *Xray) tryHotRoutes(next *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) (bool, error) {
	x.lifecycleMu.Lock()
	defer x.lifecycleMu.Unlock()
	x.mu.Lock()
	previous := x.nodeConfig
	oldUsers := append([]model.UserSpec(nil), x.users...)
	oldTLS := x.tls
	instance := x.instance
	x.mu.Unlock()
	if previous == nil || instance == nil || !tlsEqual(oldTLS, tls) || !reflect.DeepEqual(oldUsers, users) {
		return false, nil
	}
	before := buildConfig(x.cfg, previous, oldUsers, oldTLS)
	after := buildConfig(x.cfg, next, users, tls)
	// 配置文件内容可能在两次生成之间被改写，使用实际已加载配置作比较才可保证安全；自定义文件暂走完整重建。
	if x.cfg.CustomConfig != "" {
		return false, nil
	}
	if !routesOnly(before, after) {
		return false, nil
	}
	data, err := json.Marshal(buildConfig(x.cfg, next, users, tls))
	if err != nil {
		return false, err
	}
	config, err := serial.LoadJSONConfig(bytes.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("validate routes: %w", err)
	}
	r, ok := instance.GetFeature(routing.RouterType()).(routing.Router)
	if !ok {
		return false, nil
	}
	for _, app := range config.App {
		value, err := app.GetInstance()
		if err != nil {
			return false, err
		}
		if _, ok := value.(*router.Config); ok {
			if err := r.AddRule(app, false); err != nil {
				return false, fmt.Errorf("apply routes: %w", err)
			}
			x.mu.Lock()
			x.nodeConfig = next
			x.lastKernelHash = kernel.ComputeHash(next, users)
			x.mu.Unlock()
			return true, nil
		}
	}
	return false, nil
}
func routesOnly(before, after M) bool {
	for _, cfg := range []M{before, after} {
		r, ok := cfg["routing"].(M)
		if !ok {
			return false
		}
		delete(r, "rules")
		delete(r, "balancers")
	}
	return reflect.DeepEqual(before, after)
}
