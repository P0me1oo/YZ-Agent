package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ErrMachineAddressUnsupported 表示面板没有地址回报接口（面板 1.23.0 之前的版本）。
var ErrMachineAddressUnsupported = errors.New("machine address report unsupported")

const machineAddressTimeout = 15 * time.Second

// ReportMachineAddress 只经指定地址族（tcp4 或 tcp6）连接面板，面板按本次来源记录该地址族的公网地址。
// 返回面板识别到的公网地址；来源不是公网地址时为空字符串。
// 控制请求走系统默认地址族，双栈服务器上面板只能看到其中一个，所以两种地址族要分开回报。
func (c *Client) ReportMachineAddress(ctx context.Context, network string) (string, error) {
	payload := make(map[string]interface{})
	c.injectAuth(payload)
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/server/machine/address", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	client := &http.Client{
		Timeout: machineAddressTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout: 10 * time.Second,
			// 每隔几分钟才回报一次，不保留空闲连接。
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrMachineAddressUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("machine address status %d", resp.StatusCode)
	}
	var result struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.IP, nil
}
