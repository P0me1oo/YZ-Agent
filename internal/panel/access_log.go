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

type AccessLogPolicy struct {
	Version    int     `json:"version"`
	Enabled    *bool   `json:"enabled,omitempty"`
	Generation *string `json:"generation,omitempty"`
}

type accessLogState struct {
	enabled         bool
	discardRevision uint64
	generation      string
}

func (c *Client) accessLogPolicyState() *accessLogState {
	state := c.accessLog.Load()
	// 机器控制连接已收到策略时，各节点使用同一策略，迟到的节点响应不能覆盖它。
	if c.hostMonitoringParent != nil {
		if shared := c.hostMonitoringParent.accessLog.Load(); shared != nil {
			state = shared
		}
	}
	return state
}

func (c *Client) AccessLogState() (bool, uint64) {
	state := c.accessLogPolicyState()
	if state == nil {
		return false, 0
	}
	return state.enabled, state.discardRevision
}

func (c *Client) AccessLogGeneration() string {
	if state := c.accessLogPolicyState(); state != nil {
		return state.generation
	}
	return ""
}

func (c *Client) AccessLogEnabled() bool {
	enabled, _ := c.AccessLogState()
	return enabled
}

func (c *Client) applyAccessLog(policy *AccessLogPolicy) {
	if policy == nil {
		return
	}
	enabled := policy.Version == 2 && (policy.Enabled == nil || *policy.Enabled)
	for {
		previous := c.accessLog.Load()
		generation := ""
		if previous != nil {
			generation = previous.generation
		}
		if policy.Generation != nil {
			generation = *policy.Generation
		}
		if previous != nil && previous.enabled == enabled && previous.generation == generation {
			return
		}
		next := &accessLogState{enabled: enabled, generation: generation}
		if previous != nil {
			next.discardRevision = previous.discardRevision
		}
		disabled := !enabled && (previous == nil || previous.enabled)
		changedGeneration := previous != nil && previous.generation != generation
		if disabled || changedGeneration {
			next.discardRevision++
		}
		if c.accessLog.CompareAndSwap(previous, next) {
			return
		}
	}
}

func (c *Client) ReportAccessLog(ctx context.Context, batch *accesslog.Batch) error {
	state := c.accessLogPolicyState()
	if state == nil || !state.enabled {
		return errors.New("面板未启用访问记录")
	}
	if batch.Generation != state.generation {
		return errors.New("访问记录批次已因停用失效")
	}
	payload := map[string]interface{}{"batch_id": batch.ID, "records": batch.Records, "generation": batch.Generation}
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
