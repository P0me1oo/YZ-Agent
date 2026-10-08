package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

func (c *Client) DeviceHandoverSupported() bool { return c.deviceHandover.Load() }
func (c *Client) DeviceRenewalSupported() bool  { return c.deviceHandoverRenewal.Load() }

func deviceRequest[T any](c *Client, ctx context.Context, action string, data map[string]interface{}) (T, error) {
	var empty T
	if c.deviceHandoverWS.Load() {
		if ws := c.realtimeSocket(); ws != nil {
			ackCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			receipt, err := ws.Request(ackCtx, "device."+action, c.nodeID, data)
			cancel()
			if err == nil && receipt.Accepted {
				if result, decodeErr := decodeDeviceReply[T](receipt.Result); decodeErr == nil {
					return result, nil
				}
			}
			var remote *RemoteError
			if errors.As(err, &remote) {
				if remote.Code != http.StatusNotFound && remote.Code != http.StatusMethodNotAllowed {
					return empty, deviceError(err)
				}
				c.deviceHandoverWS.Store(false)
			}
			if ctx.Err() != nil {
				return empty, ctx.Err()
			}
			// 保留同一业务序号和内容切换通道，丢失确认不会变成第二次来源申请。
			c.quarantineSocket(ws)
		}
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	err := c.realtimeHTTP(ctx, http.MethodPost, "/api/v2/server/device-handover/"+action, data, &response)
	if err != nil {
		return empty, deviceError(err)
	}
	return decodeDeviceReply[T](response.Data)
}

func decodeDeviceReply[T any](data json.RawMessage) (T, error) {
	var result T
	if len(data) == 0 || string(data) == "null" {
		return result, errors.New("设备来源确认结果为空")
	}
	err := json.Unmarshal(data, &result)
	return result, err
}

func deviceError(err error) error {
	var remote *RemoteError
	if errors.As(err, &remote) && remote.Code == http.StatusConflict {
		return devicegate.ErrSessionLost
	}
	if errors.As(err, &remote) && remote.Code == http.StatusPreconditionFailed {
		return devicegate.ErrSnapshotRequired
	}
	return err
}

func (c *Client) BeginDeviceSession(ctx context.Context, run string) (devicegate.BeginReply, error) {
	return deviceRequest[devicegate.BeginReply](c, ctx, "begin", map[string]interface{}{"run": run})
}

func (c *Client) AdmitDeviceSource(ctx context.Context, request devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
	return deviceRequest[devicegate.AdmissionReply](c, ctx, "admit", map[string]interface{}{
		"run": request.Run, "sequence": request.Sequence, "user_id": request.UserID, "ip": request.IP,
	})
}

func (c *Client) SyncDeviceSession(ctx context.Context, snapshot devicegate.Snapshot) (devicegate.SyncReply, error) {
	payload := map[string]interface{}{
		"run": snapshot.Run, "sequence": snapshot.Sequence, "pending": snapshot.Pending, "sources": snapshot.Sources,
	}
	if snapshot.Retired != nil {
		payload["retired"] = snapshot.Retired
	}
	if snapshot.Unchanged {
		payload = map[string]interface{}{
			"run": snapshot.Run, "sequence": snapshot.Sequence,
			"unchanged": true, "base_sequence": snapshot.BaseSequence,
		}
	}
	response, err := deviceRequest[devicegate.SyncReply](c, ctx, "sync", payload)
	var remote *RemoteError
	if snapshot.Unchanged && errors.As(err, &remote) && remote.Code == http.StatusUnprocessableEntity {
		// 面板回滚后立即恢复完整快照，不把不支持续期误判成会话丢失。
		c.deviceHandoverRenewal.Store(false)
		err = devicegate.ErrSnapshotRequired
	}
	return response, err
}
