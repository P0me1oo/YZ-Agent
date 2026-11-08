package panel

import "maps"

// HostMonitoringEnabled 与页面采样租约独立；网络故障不撤销已收到的 Lite 策略。
func (c *Client) HostMonitoringEnabled() bool {
	if c.hostMonitoringParent != nil {
		return c.hostMonitoringParent.HostMonitoringEnabled()
	}
	return !c.hostMonitoringDisabled.Load()
}

func (c *Client) applyHostMonitoring(enabled *bool) {
	// 机器内所有节点共用机器控制策略，节点的迟到响应不能覆盖它。
	if c.hostMonitoringParent == nil {
		c.hostMonitoringDisabled.Store(enabled != nil && !*enabled)
	}
}

// 发送前再过滤一次，避免 Lite 切换后重试旧批次时继续携带主机采样。
func (c *Client) filterHostState(state StatePayload) StatePayload {
	if c.HostMonitoringEnabled() {
		return state
	}
	if state.Status != nil {
		status := make(map[string]interface{})
		if value, ok := state.Status["kernel_status"]; ok {
			status["kernel_status"] = value
		}
		state.Status = nil
		if len(status) > 0 {
			state.Status = status
		}
	}
	if state.Metrics != nil {
		state.Metrics = maps.Clone(state.Metrics)
		delete(state.Metrics, "cpu_per_core")
		delete(state.Metrics, "load")
	}
	return state
}
