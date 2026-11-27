package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/healthcheck"
)

func (c *Client) ReportHealth(ctx context.Context, result *healthcheck.Result) error {
	c.realtime.stateMu.Lock()
	epoch := c.realtime.version.Epoch
	c.realtime.stateMu.Unlock()
	if epoch == "" {
		return fmt.Errorf("runtime session not ready")
	}
	payload := map[string]any{"epoch": epoch, "health": result}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	response, err := c.doRequestContext(ctx, http.MethodPost, "/api/v2/server/health/report", body, "")
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health report status %d", response.StatusCode)
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
		return err
	}
	if !receipt.Accepted {
		return fmt.Errorf("health result superseded")
	}
	return nil
}
