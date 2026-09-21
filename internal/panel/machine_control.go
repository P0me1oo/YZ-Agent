package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
)

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
	payload := map[string]interface{}{"version": version, "boot_id": bootID, "manageable": manageable}
	if operation != nil {
		payload["operation"] = operation
	}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequest("POST", "/api/v2/server/machine/control", body, "")
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)
	// 旧面板不支持控制接口时，继续原有节点服务。
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("machine control status %d", resp.StatusCode)
	}
	var result struct {
		Command *MachineOperation `json:"command"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Command, nil
}
