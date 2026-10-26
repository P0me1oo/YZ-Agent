package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/P0me1oo/YZ-Agent/internal/nlog"
	"io"
	"net/http"
)

type AgentLogBatch struct {
	ID      string       `json:"batch_id"`
	Records []nlog.Event `json:"records"`
}

func (c *Client) AgentLogEnabled() bool { return c.agentLog.Load() && !nlog.LogsDisabled() }
func containsXrayCrash(events []string) bool {
	for _, event := range events {
		if event == "xray.crash" {
			return true
		}
	}
	return false
}
func (c *Client) ReportAgentLog(ctx context.Context, batch *AgentLogBatch) error {
	if nlog.LogsDisabled() {
		return errors.New("运行日志已停用")
	}
	records := batch.Records
	// 旧面板只接受原日志字段；保留本地批次，按握手能力省略事件标记。
	if !c.agentLogEvents.Load() {
		records = append([]nlog.Event(nil), records...)
		for i := range records {
			records[i].Event = ""
		}
	}
	payload := map[string]any{"batch_id": batch.ID, "records": records}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := "/api/v2/server/agent-log"
	if c.machineID > 0 && c.nodeID == 0 {
		endpoint = "/api/v2/server/machine/agent-log"
	}
	resp, err := c.doRequestContext(ctx, "POST", endpoint, body, "")
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Agent 日志上报失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			BatchID string `json:"batch_id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result) != nil || result.Data.BatchID != batch.ID {
		return errors.New("Agent 日志确认不匹配")
	}
	return nil
}
