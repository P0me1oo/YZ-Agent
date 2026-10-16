package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/accesslog"
)

func (c *Client) AccessLogEnabled() bool { return c.accessLog.Load() }

func (c *Client) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	if !c.AccessLogEnabled() {
		return errors.New("面板未启用访问记录")
	}
	payload := map[string]interface{}{"batch_id": batch.ID, "records": batch.Records}
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := c.doRequestContext(ctx, "POST", "/api/v2/server/access-log", body, "")
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	// 错误回复可能回显连接信息，只记录状态码。
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("访问记录上报失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			BatchID string `json:"batch_id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		return errors.New("访问记录确认格式无效")
	}
	if result.Data.BatchID != batch.ID {
		return errors.New("访问记录确认批次不一致")
	}
	return nil
}
