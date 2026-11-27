package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/binding"
	"github.com/P0me1oo/YZ-Agent/internal/monitor"
	"github.com/P0me1oo/YZ-Agent/internal/realityscan"
)

// SetBindingDeletionHandler is configured before starting the machine control loop.
func (c *Client) SetBindingDeletionHandler(handle func(binding.Confirmation)) {
	c.onBindingDeleted = handle
}

type MachineOperation struct {
	ID        string `json:"id"`
	Action    string `json:"action,omitempty"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	Result    string `json:"result,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

// ExchangeMachineControl 独立于节点配置和负载采样，空服务器也能接受操作。
func (c *Client) ExchangeMachineControl(version, bootID string, manageable bool, operation *MachineOperation) (*MachineOperation, error) {
	command, _, err := c.exchangeMachineControl(context.Background(), version, bootID, manageable, operation, nil, false)
	return command, err
}

// 检测能力独立于远程重启权限，旧面板忽略新增字段后仍可正常工作。
func (c *Client) ExchangeMachineControlWithReality(version, bootID string, manageable bool, operation *MachineOperation, scan *realityscan.Report) (*MachineOperation, *realityscan.Task, error) {
	return c.exchangeMachineControl(context.Background(), version, bootID, manageable, operation, scan, true)
}

func (c *Client) ExchangeMachineControlContext(ctx context.Context, version, bootID string, manageable bool, operation *MachineOperation, scan *realityscan.Report) (*MachineOperation, *realityscan.Task, error) {
	return c.exchangeMachineControl(ctx, version, bootID, manageable, operation, scan, true)
}

func (c *Client) exchangeMachineControl(ctx context.Context, version, bootID string, manageable bool, operation *MachineOperation, scan *realityscan.Report, supported bool) (*MachineOperation, *realityscan.Task, error) {
	payload := map[string]interface{}{"version": version, "boot_id": bootID, "manageable": manageable, "tcp_congestion": monitor.TCPCongestion()}
	var nonce string
	if c.machineID > 0 && c.nodeID == 0 && c.onBindingDeleted != nil {
		nonce = binding.Challenge(c.baseURL)
		if nonce != "" {
			payload["nonce"] = nonce
		}
	}
	var sampledAt int64
	if c.hostMetricsEnabled.Load() && c.nodeID == 0 && c.machineID > 0 {
		sample := monitor.CollectHostSample()
		if sample.SampledAt != c.lastHostSample.Load() {
			payload["host_metrics"] = sample
			sampledAt = sample.SampledAt
		}
	}
	if supported {
		payload["reality_scan_supported"] = true
	}
	if scan != nil {
		payload["reality_scan"] = scan
	}
	if operation != nil {
		payload["operation"] = operation
	}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.doRequestContext(ctx, "POST", "/api/v2/server/machine/control", body, "")
	if err != nil {
		return nil, nil, err
	}
	defer drainAndClose(resp.Body)
	// 旧面板不支持控制接口时，继续原有节点服务。
	if resp.StatusCode == http.StatusNotFound {
		c.hostMetricsEnabled.Store(false)
		return nil, nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("machine control status %d", resp.StatusCode)
	}
	var result struct {
		Deletion           *binding.Proof    `json:"binding_deletion"`
		HostMetricsVersion int               `json:"host_metrics_version"`
		AccessLog          *AccessLogPolicy  `json:"access_log,omitempty"`
		HostMonitoring     *bool             `json:"host_monitoring"`
		LogPolicy          *LogPolicy        `json:"log_policy,omitempty"`
		Command            *MachineOperation `json:"command"`
		RealityScan        *realityscan.Task `json:"reality_scan"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}
	if result.Deletion != nil {
		if resp.Request != nil && resp.Request.Response == nil {
			if confirmed, ok := result.Deletion.Verify(c.baseURL, c.machineID, c.token, nonce); ok {
				c.onBindingDeleted(confirmed)
			}
		}
		return nil, nil, nil
	}
	// Only machine/control negotiates this stream. Node telemetry receipts
	// and legacy host monitoring switches belong to separate protocols.
	c.hostMetricsEnabled.Store(result.HostMetricsVersion == 1)
	if sampledAt != 0 && result.HostMetricsVersion == 1 {
		c.lastHostSample.Store(sampledAt)
	}
	c.applyLogPolicy(result.LogPolicy)
	c.applyHostMonitoring(result.HostMonitoring)
	c.applyAccessLog(result.AccessLog)
	return result.Command, result.RealityScan, nil
}
