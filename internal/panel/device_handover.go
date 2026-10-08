package panel

import (
	"context"
	"errors"
	"net/http"

	"github.com/P0me1oo/YZ-Agent/internal/devicegate"
)

func (c *Client) DeviceHandoverSupported() bool { return c.deviceHandover.Load() }

func (c *Client) deviceRequest(ctx context.Context, action string, data map[string]interface{}, output interface{}) error {
	err := c.realtimeHTTP(ctx, http.MethodPost, "/api/v2/server/device-handover/"+action, data, output)
	var remote *RemoteError
	if errors.As(err, &remote) && remote.Code == http.StatusConflict {
		return devicegate.ErrSessionLost
	}
	return err
}

func (c *Client) BeginDeviceSession(ctx context.Context, run string) (devicegate.BeginReply, error) {
	var response struct {
		Data devicegate.BeginReply `json:"data"`
	}
	err := c.deviceRequest(ctx, "begin", map[string]interface{}{"run": run}, &response)
	return response.Data, err
}

func (c *Client) AdmitDeviceSource(ctx context.Context, request devicegate.AdmissionRequest) (devicegate.AdmissionReply, error) {
	var response struct {
		Data devicegate.AdmissionReply `json:"data"`
	}
	err := c.deviceRequest(ctx, "admit", map[string]interface{}{
		"run": request.Run, "sequence": request.Sequence, "user_id": request.UserID, "ip": request.IP,
	}, &response)
	return response.Data, err
}

func (c *Client) SyncDeviceSession(ctx context.Context, snapshot devicegate.Snapshot) (devicegate.SyncReply, error) {
	var response struct {
		Data devicegate.SyncReply `json:"data"`
	}
	payload := map[string]interface{}{
		"run": snapshot.Run, "sequence": snapshot.Sequence, "pending": snapshot.Pending, "sources": snapshot.Sources,
	}
	if snapshot.Retired != nil {
		payload["retired"] = snapshot.Retired
	}
	err := c.deviceRequest(ctx, "sync", payload, &response)
	return response.Data, err
}
