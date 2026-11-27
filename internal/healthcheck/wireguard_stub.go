//go:build !with_wireguard

package healthcheck

import (
	"context"
	"errors"
)

func wireGuardClient(context.Context, any) (DialFunc, func(), error) {
	return nil, nil, errors.New("当前构建不含 WireGuard 客户端")
}
