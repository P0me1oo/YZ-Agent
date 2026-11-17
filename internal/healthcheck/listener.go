package healthcheck

import (
	"context"
	"net/netip"
	"os"
	"runtime"
	"strconv"

	gcommon "github.com/shirou/gopsutil/v4/common"
	gnet "github.com/shirou/gopsutil/v4/net"
)

// Listener 核对当前进程拥有的 TCP 监听，不发送会触发入站认证错误的空连接。
func Listener(ctx context.Context, address string) (bool, error) {
	return listener(
		ctx,
		address,
		int32(os.Getpid()),
		processConnections,
	)
}

func listener(
	ctx context.Context,
	address string,
	pid int32,
	read func(context.Context, int32) ([]gnet.ConnectionStat, error),
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	wanted, err := netip.ParseAddrPort(address)
	if err != nil {
		return false, err
	}
	connections, err := read(ctx, pid)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, connection := range connections {
		if connection.Pid != pid || connection.Status != "LISTEN" || connection.Laddr.Port != uint32(wanted.Port()) {
			continue
		}
		bound, err := netip.ParseAddr(connection.Laddr.IP)
		if err != nil {
			continue
		}
		bound, target := bound.Unmap(), wanted.Addr().Unmap()
		if bound == target || bound.IsUnspecified() && bound.Is4() == target.Is4() {
			return true, nil
		}
	}
	return false, nil
}

func processConnections(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
	if runtime.GOOS == "linux" {
		// 检查的是实际运行进程；外部主机监控的 HOST_PROC 不能改变监听归属。
		ctx = context.WithValue(ctx, gcommon.EnvKey, gcommon.EnvMap{gcommon.HostProcEnvKey: "/proc"})
		fd, err := os.Open("/proc/" + strconv.Itoa(int(pid)) + "/fd")
		if err != nil {
			return nil, err
		}
		fd.Close()
		return gnet.ConnectionsPidWithoutUidsWithContext(ctx, "tcp", pid)
	}
	return gnet.ConnectionsPidWithContext(ctx, "tcp", pid)
}
