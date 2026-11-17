//go:build !linux

package healthcheck

import (
	"context"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func processConnections(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
	return gnet.ConnectionsPidWithContext(ctx, "tcp", pid)
}
