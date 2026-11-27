//go:build !linux

package healthcheck

import (
	"context"
	"errors"
	"os"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func processConnections(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
	return processNetworkConnections(ctx, pid, "tcp")
}
func processNetworkConnections(ctx context.Context, pid int32, network string) ([]gnet.ConnectionStat, error) {
	return gnet.ConnectionsPidWithContext(ctx, network, pid)
}

func dualStackListener(connection gnet.ConnectionStat) (bool, error) {
	if connection.Pid != int32(os.Getpid()) {
		return false, nil
	}
	return false, errors.New("当前平台无法核对双栈监听设置")
}
