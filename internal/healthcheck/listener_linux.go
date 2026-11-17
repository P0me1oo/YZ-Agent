//go:build linux

package healthcheck

import (
	"context"
	"os"
	"runtime"
	"strconv"

	gcommon "github.com/shirou/gopsutil/v4/common"
	gnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/unix"
)

func processConnections(ctx context.Context, pid int32) ([]gnet.ConnectionStat, error) {
	// 外部主机监控的 HOST_PROC 不能改变实际运行进程的监听归属。
	ctx = context.WithValue(ctx, gcommon.EnvKey, gcommon.EnvMap{gcommon.HostProcEnvKey: "/proc"})
	// WireGuard 空间创建可能结束主线程；其连接表不能代表当前监听空间。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	readPID := pid
	if pid == int32(os.Getpid()) {
		readPID = int32(unix.Gettid())
	}
	fd, err := os.Open("/proc/" + strconv.Itoa(int(readPID)) + "/fd")
	if err != nil {
		return nil, err
	}
	fd.Close()
	connections, err := gnet.ConnectionsPidWithoutUidsWithContext(ctx, "tcp", readPID)
	if err != nil {
		return nil, err
	}
	// 同一线程组共用描述符，仍向调用方提供进程编号而非线程编号。
	for index := range connections {
		if connections[index].Pid == readPID {
			connections[index].Pid = pid
		}
	}
	return connections, nil
}
