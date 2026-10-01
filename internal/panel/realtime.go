package panel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const realtimeFallbackInterval = 10 * time.Second

// StatePayload 只含当前状态，不含会累计结算的流量或超限事件。
type StatePayload struct {
	Alive                 map[int][]string         `json:"alive,omitempty"`
	Online                map[int]int              `json:"online,omitempty"`
	ConnectionCounts      map[int]int              `json:"connection_counts,omitempty"`
	UserSpeeds            map[int][2]int64         `json:"user_speeds,omitempty"`
	RelayUserAlive        map[int]map[int][]string `json:"relay_user_alive,omitempty"`
	RelayConnectionCounts map[int]map[int]int      `json:"relay_connection_counts,omitempty"`
	Status                map[string]interface{}   `json:"status,omitempty"`
	Metrics               map[string]interface{}   `json:"metrics,omitempty"`
}

// mapValue 保留非 nil 的空快照，空状态与不支持该采样不是一回事。
func (s StatePayload) mapValue() map[string]interface{} {
	out := make(map[string]interface{})
	if s.Alive != nil {
		out["alive"] = s.Alive
	}
	if s.Online != nil {
		out["online"] = s.Online
	}
	if s.ConnectionCounts != nil {
		out["connection_counts"] = s.ConnectionCounts
	}
	if s.UserSpeeds != nil {
		out["user_speeds"] = s.UserSpeeds
	}
	if s.RelayUserAlive != nil {
		out["relay_user_alive"] = s.RelayUserAlive
	}
	if s.RelayConnectionCounts != nil {
		out["relay_connection_counts"] = s.RelayConnectionCounts
	}
	if s.Status != nil {
		out["status"] = s.Status
	}
	if s.Metrics != nil {
		out["metrics"] = s.Metrics
	}
	return out
}

type realtimeClient struct {
	enabled      atomic.Bool
	stateMu      sync.Mutex
	run          string
	version      StateVersion
	lastFallback time.Time

	pushMu           sync.Mutex
	push             func() *WSClient
	failedSocket     *WSClient
	failedGeneration uint64
	retryAfter       time.Time
}

func (c *Client) RealtimeEnabled() bool { return c.realtime.enabled.Load() }

// SetWebSocketProvider 让机器内各节点取得当前共享连接，而不是固定旧连接指针。
func (c *Client) SetWebSocketProvider(provider func() *WSClient) {
	c.realtime.pushMu.Lock()
	c.realtime.push = provider
	c.realtime.pushMu.Unlock()
}

func (c *Client) realtimeSocket() *WSClient {
	c.realtime.pushMu.Lock()
	provider := c.realtime.push
	c.realtime.pushMu.Unlock()
	if provider == nil {
		return nil
	}
	ws := provider()
	if ws == nil || !ws.IsConnected() {
		return nil
	}
	c.realtime.pushMu.Lock()
	defer c.realtime.pushMu.Unlock()
	if c.realtime.failedSocket == ws && c.realtime.failedGeneration == ws.Generation() && time.Now().Before(c.realtime.retryAfter) {
		return nil
	}
	return ws
}

func (c *Client) quarantineSocket(ws *WSClient) {
	c.realtime.pushMu.Lock()
	c.realtime.failedSocket = ws
	c.realtime.failedGeneration = ws.Generation()
	c.realtime.retryAfter = time.Now().Add(realtimeFallbackInterval)
	c.realtime.pushMu.Unlock()
}

func (c *Client) runtimePath(action string) string {
	if c.machineID > 0 && c.nodeID == 0 {
		return "/api/v2/server/machine/realtime/" + action
	}
	return "/api/v2/server/realtime/" + action
}

// PublishState 由独立状态发送任务调用。HTTP 兜底期间合并采样，只发送最新状态。
func (c *Client) PublishState(ctx context.Context, state StatePayload) error {
	if !c.RealtimeEnabled() {
		return nil
	}
	c.realtime.stateMu.Lock()
	defer c.realtime.stateMu.Unlock()
	ws := c.realtimeSocket()
	if ws == nil && time.Since(c.realtime.lastFallback) < realtimeFallbackInterval {
		return nil
	}
	if c.realtime.version.Epoch == "" {
		if c.realtime.run == "" {
			var value [16]byte
			if _, err := rand.Read(value[:]); err != nil {
				return err
			}
			c.realtime.run = hex.EncodeToString(value[:])
		}
		var response struct {
			Data StateVersion `json:"data"`
		}
		if err := c.realtimeHTTP(ctx, http.MethodPost, c.runtimePath("begin"), map[string]interface{}{"run": c.realtime.run}, &response); err != nil {
			return err
		}
		if response.Data.Epoch == "" {
			return fmt.Errorf("panel returned an empty runtime session")
		}
		c.realtime.version = response.Data
	}
	c.realtime.version.Sequence++
	version := c.realtime.version
	payload := map[string]interface{}{"epoch": version.Epoch, "sequence": version.Sequence, "state": state.mapValue()}
	if ws != nil {
		event := "runtime.state"
		if c.nodeID == 0 {
			event = "machine.state"
		}
		ackCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		receipt, err := ws.Request(ackCtx, event, c.nodeID, payload)
		cancel()
		if err == nil && receipt.Epoch == version.Epoch && receipt.Sequence >= version.Sequence {
			c.realtime.lastFallback = time.Time{}
			return nil
		}
		c.quarantineSocket(ws)
	}
	c.realtime.lastFallback = time.Now()
	var response struct {
		Data RPCReceipt `json:"data"`
	}
	if err := c.realtimeHTTP(ctx, http.MethodPost, c.runtimePath("state"), payload, &response); err != nil {
		var remote *RemoteError
		if errors.As(err, &remote) && remote.Code == http.StatusConflict {
			c.realtime.version = StateVersion{}
			c.realtime.lastFallback = time.Time{}
		}
		return err
	}
	if response.Data.Epoch != version.Epoch || response.Data.Sequence < version.Sequence {
		return fmt.Errorf("panel did not confirm the state version")
	}
	return nil
}

func (c *Client) reportRealtime(ctx context.Context, original map[string]interface{}) error {
	// 流量重试不再携带原批次中的旧设备和负载状态。
	payload := make(map[string]interface{})
	for _, name := range []string{"report_id", "traffic", "relay_traffic", "relay_user_traffic", "limit_events"} {
		if value, ok := original[name]; ok {
			payload[name] = value
		}
	}
	reportID, _ := payload["report_id"].(string)
	if reportID == "" {
		return fmt.Errorf("realtime traffic requires a batch identifier")
	}
	if ws := c.realtimeSocket(); ws != nil {
		ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		receipt, err := ws.Request(ackCtx, "report.traffic", c.nodeID, payload)
		cancel()
		if err == nil && receipt.Accepted && receipt.ReportID == reportID {
			return nil
		}
		c.quarantineSocket(ws)
	}
	payload["realtime"] = true
	var response struct {
		Receipt RPCReceipt `json:"receipt"`
	}
	if err := c.realtimeHTTP(ctx, http.MethodPost, "/api/v2/server/report", payload, &response); err != nil {
		return err
	}
	if !response.Receipt.Accepted || response.Receipt.ReportID != reportID {
		return fmt.Errorf("panel did not confirm the traffic batch")
	}
	return nil
}

func (c *Client) realtimeHTTP(ctx context.Context, method, path string, payload map[string]interface{}, output interface{}) error {
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var body []byte
	if method != http.MethodGet {
		copyPayload := make(map[string]interface{}, len(payload)+3)
		for key, value := range payload {
			copyPayload[key] = value
		}
		c.injectAuth(copyPayload)
		var err error
		body, err = json.Marshal(copyPayload)
		if err != nil {
			return err
		}
	}
	response, err := c.doRequestContext(requestCtx, method, path, body, "")
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusOK {
		return &RemoteError{Code: response.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 10<<20)).Decode(output); err != nil {
		return fmt.Errorf("decode realtime response: %w", err)
	}
	return nil
}

// ControlSnapshot 是两种通道共用的完整配置和用户基线。
type ControlSnapshot struct {
	StateVersion
	NodeID int        `json:"node_id"`
	Config NodeConfig `json:"config"`
	Users  []User     `json:"users"`
}

type ControlState struct {
	Control ControlSnapshot    `json:"control"`
	Devices syncDevicesPayload `json:"devices"`
}

func (c *Client) GetControlState(ctx context.Context) (*ControlState, error) {
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := c.realtimeHTTP(ctx, http.MethodGet, c.runtimePath("sync"), nil, &response); err != nil {
		return nil, err
	}
	var raw struct {
		Control map[string]interface{} `json:"control"`
		Devices syncDevicesPayload     `json:"devices"`
	}
	if err := json.Unmarshal(response.Data, &raw); err != nil {
		return nil, err
	}
	result := &ControlState{Devices: raw.Devices}
	if err := decodeWeakRaw(raw.Control, &result.Control); err != nil {
		return nil, err
	}
	var versions struct {
		Control StateVersion `json:"control"`
	}
	if err := json.Unmarshal(response.Data, &versions); err != nil {
		return nil, err
	}
	result.Control.StateVersion = versions.Control
	if result.Control.Epoch == "" || result.Control.Config.Protocol == "" {
		return nil, fmt.Errorf("invalid realtime control snapshot")
	}
	if result.Control.Users == nil {
		result.Control.Users = []User{}
	}
	return result, nil
}
