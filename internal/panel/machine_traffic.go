package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/machinetraffic"
)

func (c *Client) ExchangeMachineTraffic(ctx context.Context, sample *machinetraffic.Sample) (*machinetraffic.Policy, error) {
	payload := map[string]interface{}{}
	if sample != nil {
		payload["sample"] = sample
	}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequestContext(ctx, "POST", "/api/v2/server/machine/traffic", body, "")
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("machine traffic status %d", resp.StatusCode)
	}
	var result struct {
		Traffic *machinetraffic.Policy `json:"traffic"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Traffic == nil {
		return nil, fmt.Errorf("missing machine traffic policy")
	}
	return result.Traffic, nil
}
