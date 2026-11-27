package healthcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	singJSON "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
)

func ForwardClient(ctx context.Context, raw any) (DialFunc, func(), error) {
	if bytes, ok := raw.([]byte); ok {
		raw = json.RawMessage(bytes)
	}
	config := map[string]any{"log": map[string]any{"disabled": true}, "outbounds": []any{raw}}
	if m, ok := raw.(map[string]any); ok && m["engine"] == "singbox-endpoint" {
		return wireGuardClient(ctx, m["endpoint"])
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, nil, err
	}
	ctx = include.Context(ctx)
	options, err := singJSON.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return nil, nil, fmt.Errorf("转发协议配置无法解析: %w", err)
	}
	client, err := box.New(box.Options{Context: ctx, Options: options})
	if err != nil {
		return nil, nil, fmt.Errorf("转发检查客户端无法创建: %w", err)
	}
	if err := client.Start(); err != nil {
		client.Close()
		return nil, nil, err
	}
	outbound := client.Outbound().Default()
	if outbound == nil {
		client.Close()
		return nil, nil, fmt.Errorf("转发检查缺少协议出站")
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return outbound.DialContext(ctx, network, M.ParseSocksaddr(address))
	}, func() { client.Close() }, nil
}
