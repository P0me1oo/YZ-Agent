package config

import (
	"fmt"
	"path/filepath"

	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"github.com/P0me1oo/YZ-Agent/internal/privacy"
)

func (c *Config) RuntimeLogState() *privacy.State {
	if c.logState != nil {
		return c.logState
	}
	return c.privateState
}

func (c *Config) RegisterRuntimeLogs() {
	if c.IsStandalone() {
		return
	}
	machineID := c.Panel.MachineID
	if c.Machine != nil {
		machineID = c.Machine.MachineID
	}
	path := ""
	if c.Kernel.ConfigDir != "" {
		path = filepath.Join(c.Kernel.ConfigDir, "runtime.log.enc")
	}
	policy := nlog.LogPolicyKey(c.Panel.URL, machineID, c.Panel.NodeID)
	state := c.RuntimeLogState()
	nlog.RegisterLogScope(
		c.LogScope(),
		policy,
		path,
		state,
	)
	if state != nil {
		// 密钥接口已认证并确认策略，启动日志不必等后续握手；缓存恢复保持加密。
		private := c.logPrivate || c.logState == nil && c.privateState != nil
		nlog.ApplyLogPolicy(policy, private)
	}
}

// 同一机器内的节点共用上报身份，独立节点各自隔离；不把凭据放入标识。
func (c *Config) LogScope() string {
	machineID := c.Panel.MachineID
	if c.Machine != nil {
		machineID = c.Machine.MachineID
	}
	if machineID > 0 {
		return fmt.Sprintf("%s|%s|machine:%d", c.InstanceID, c.Panel.URL, machineID)
	}
	return fmt.Sprintf("%s|%s|node:%d", c.InstanceID, c.Panel.URL, c.Panel.NodeID)
}
