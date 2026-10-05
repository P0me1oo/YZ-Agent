//go:build !linux

package xray

import (
	"context"
	"github.com/P0me1oo/YZ-Agent/internal/systemwg"
	xrayCore "github.com/xtls/xray-core/core"
)

func systemWGContext(ctx context.Context, _ *systemwg.Runtime) context.Context    { return ctx }
func attachSystemWG(context.Context, *xrayCore.Instance, *systemwg.Runtime) error { return nil }
