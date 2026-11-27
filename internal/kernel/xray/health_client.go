package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"net"

	xnet "github.com/xtls/xray-core/common/net"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/infra/conf/serial"
)

// DiagnosticClient uses the same pinned transport implementation as the running
// core. It has no listener and exists only for one bounded inspection.
func DiagnosticClient(ctx context.Context, outbound any) (func(context.Context, string, string) (net.Conn, error), func(), error) {
	raw, err := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "outbounds": []any{outbound}})
	if err != nil {
		return nil, nil, err
	}
	cfg, err := serial.LoadJSONConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	xrayCreationMu.Lock()
	instance, err := xcore.NewWithContext(ctx, cfg)
	xrayCreationMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if err := instance.Start(); err != nil {
		instance.Close()
		return nil, nil, err
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		dest, err := xnet.ParseDestination(network + ":" + address)
		if err != nil {
			return nil, err
		}
		return xcore.Dial(context.WithValue(ctx, healthProbeContextKey{}, true), instance, dest)
	}, func() { instance.Close() }, nil
}
