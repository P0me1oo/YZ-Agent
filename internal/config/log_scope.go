package config

import "fmt"

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
